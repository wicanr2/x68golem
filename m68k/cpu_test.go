package m68k

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestMC68000Level4AutovectorWakesSTOP(t *testing.T) {
	memory := SparseMemory{
		0x70: 0x00, 0x71: 0xfc, 0x72: 0x04, 0x73: 0x46,
		0xfc0446: 0x52, 0xfc0447: 0xb8, 0xfc0448: 0x04, 0xfc0449: 0x66,
		0x1000: 0x4e, 0x1001: 0x72, 0x1002: 0x23, 0x1003: 0x00,
	}
	cpu := CPU{Bus: memory, State: State{SSP: 0x0f70, SR: 0x2300, PC: 0x1004,
		Prefetch: [2]uint16{0x4e72, 0x2300}}}
	if _, err := cpu.Step(); err != nil {
		t.Fatal(err)
	}
	result, accepted, err := cpu.AcceptAutovector(4)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted || result.Clocks != 44 || cpu.IsStopped() {
		t.Fatalf("accepted=%v clocks=%d stopped=%v", accepted, result.Clocks, cpu.IsStopped())
	}
	if cpu.State.SSP != 0x0f6a || cpu.State.SR != 0x2400 || cpu.State.PC != 0xfc044a ||
		cpu.State.Prefetch != [2]uint16{0x52b8, 0x0466} {
		t.Fatalf("unexpected state: %+v", cpu.State)
	}
	if memory[0x0f6a] != 0x23 || memory[0x0f6b] != 0x00 || memory[0x0f6c] != 0 || memory[0x0f6d] != 0 ||
		memory[0x0f6e] != 0x10 || memory[0x0f6f] != 0x04 {
		t.Fatalf("unexpected frame bytes at SSP: %v", memory)
	}
}

func TestMC68000AutovectorRejectsMaskedAndInvalidLevels(t *testing.T) {
	initial := State{SSP: 0x1000, SR: 0x2400, PC: 0x2004}
	cpu := CPU{Bus: SparseMemory{}, State: initial, stopped: true}
	if result, accepted, err := cpu.AcceptAutovector(4); err != nil || accepted || result.Clocks != 0 || len(result.Transactions) != 0 || len(result.Timeline) != 0 || cpu.State != initial || !cpu.IsStopped() {
		t.Fatalf("masked interrupt changed state: result=%+v accepted=%v err=%v", result, accepted, err)
	}
	for _, level := range []uint8{0, 7, 8} {
		if result, accepted, err := cpu.AcceptAutovector(level); !errors.Is(err, ErrInvalidAutovectorLevel) || accepted || result.Clocks != 0 || len(result.Transactions) != 0 || len(result.Timeline) != 0 || cpu.State != initial || !cpu.IsStopped() {
			t.Fatalf("level %d did not fail closed: result=%+v accepted=%v err=%v", level, result, accepted, err)
		}
	}
}

func TestMC68000AutovectorTimedBus(t *testing.T) {
	bus := &timedRecordingBus{SparseMemory: SparseMemory{
		0x70: 0x00, 0x71: 0x00, 0x72: 0x20, 0x73: 0x00,
		0x2000: 0x4e, 0x2001: 0x71, 0x2002: 0x4e, 0x2003: 0x71,
	}}
	cpu := CPU{Bus: bus, State: State{SSP: 0x1000, SR: 0x2000, PC: 0x3000}}
	result, accepted, err := cpu.AcceptAutovectorAt(4, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted || result.Clocks != 44 || len(result.Transactions) != 7 || len(result.Timeline) != 8 ||
		result.Timeline[0] != (BusPhase{Cycles: 16}) || bus.accesses[0].Clock != 116 {
		t.Fatalf("unexpected timed result=%+v accesses=%+v", result, bus.accesses)
	}
	if bus.SparseMemory[0x0ffc] != 0x00 || bus.SparseMemory[0x0ffd] != 0x00 ||
		bus.SparseMemory[0x0ffe] != 0x2f || bus.SparseMemory[0x0fff] != 0xfc {
		t.Fatalf("running interrupt saved PC bytes=%02x%02x%02x%02x want 00002ffc",
			bus.SparseMemory[0x0ffc], bus.SparseMemory[0x0ffd], bus.SparseMemory[0x0ffe], bus.SparseMemory[0x0fff])
	}
}

func TestMC68000VectoredInterruptUsesDeviceVector(t *testing.T) {
	memory := SparseMemory{
		0x110: 0x00, 0x111: 0x00, 0x112: 0x40, 0x113: 0x00,
		0x4000: 0x4e, 0x4001: 0x71, 0x4002: 0x60, 0x4003: 0xfe,
	}
	cpu := CPU{Bus: memory, State: State{SSP: 0x1000, SR: 0x2300, PC: 0x3004,
		Prefetch: [2]uint16{0x4e71, 0x60fe}}}
	result, accepted, err := cpu.AcceptVectoredInterruptAt(6, 68, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted || result.Clocks != 44 || cpu.State.SR != 0x2600 || cpu.State.SSP != 0x0ffa ||
		cpu.State.PC != 0x4004 || cpu.State.Prefetch != [2]uint16{0x4e71, 0x60fe} {
		t.Fatalf("result=%+v accepted=%v state=%+v", result, accepted, cpu.State)
	}
	if memory[0x0ffc] != 0 || memory[0x0ffd] != 0 || memory[0x0ffe] != 0x30 || memory[0x0fff] != 0 {
		t.Fatalf("saved PC bytes=%02x%02x%02x%02x want 00003000",
			memory[0x0ffc], memory[0x0ffd], memory[0x0ffe], memory[0x0fff])
	}
}

func TestMC68000STOPStateAndReset(t *testing.T) {
	initial := State{D: [8]uint32{1}, A: [7]uint32{2}, USP: 3, SSP: 4,
		SR: 0x2704, PC: 0x1004, Prefetch: [2]uint16{0x4e72, 0x2300}}
	cpu := CPU{Bus: SparseMemory{}, State: initial}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	want := initial
	want.SR = 0x2300
	if result.Clocks != 4 || len(result.Transactions) != 0 || cpu.State != want || !cpu.IsStopped() {
		t.Fatalf("result=%+v state=%+v stopped=%v", result, cpu.State, cpu.IsStopped())
	}
	stoppedState := cpu.State
	if result, err := cpu.StepAt(1234); !errors.Is(err, ErrStopped) || result.Clocks != 0 ||
		len(result.Transactions) != 0 || len(result.Timeline) != 0 ||
		cpu.State != stoppedState || !cpu.IsStopped() {
		t.Fatalf("stopped step result=%+v err=%v state=%+v stopped=%v",
			result, err, cpu.State, cpu.IsStopped())
	}

	cpu.Bus = SparseMemory{
		0: 0x00, 1: 0x00, 2: 0x30, 3: 0x00,
		4: 0x00, 5: 0x00, 6: 0x10, 7: 0x00,
		0x1000: 0x4e, 0x1001: 0x71, 0x1002: 0x4e, 0x1003: 0x71,
	}
	if err := cpu.Reset(); err != nil {
		t.Fatal(err)
	}
	if cpu.IsStopped() {
		t.Fatal("Reset did not clear stopped latch")
	}
}

func TestMC68000STOPUserModePrivilegeViolationDoesNotStop(t *testing.T) {
	memory := SparseMemory{
		0x0020: 0x00, 0x0021: 0x00, 0x0022: 0x20, 0x0023: 0x00,
		0x2000: 0x4e, 0x2001: 0x71, 0x2002: 0x4e, 0x2003: 0x71,
	}
	cpu := CPU{Bus: memory, State: State{USP: 0x4000, SSP: 0x3000, SR: 0x0004,
		PC: 0x1004, Prefetch: [2]uint16{0x4e72, 0x2700}}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if result.Clocks != 34 || cpu.IsStopped() || cpu.State.SR != 0x2004 ||
		cpu.State.SSP != 0x2ffa || cpu.State.PC != 0x2004 {
		t.Fatalf("result=%+v state=%+v stopped=%v", result, cpu.State, cpu.IsStopped())
	}
}

type resetRead struct {
	address uint32
	fc      uint8
}

type resetRecordingBus struct {
	SparseMemory
	reads          []resetRead
	failAddress    uint32
	externalResets int
	resetErr       error
}

type typedWordReadFault struct {
	address uint32
	fc      uint8
}

func (f typedWordReadFault) Error() string {
	return fmt.Sprintf("forced word read fault at 0x%x", f.address)
}

func (f typedWordReadFault) M68KBusFault() (uint32, uint8, bool, uint8) {
	return f.address, f.fc, false, 2
}

type wordReadFaultBus struct {
	SparseMemory
	faultAddress uint32
}

type timedRecordingBus struct {
	SparseMemory
	accesses      []BusAccess
	wait          uint32
	exactWordRead uint32
}

func (b *timedRecordingBus) HasExactByteWriteTiming(uint32) bool { return true }
func (b *timedRecordingBus) HasExactWordWriteTiming(uint32) bool { return true }
func (b *timedRecordingBus) HasExactWordReadTiming(address uint32) bool {
	return address == b.exactWordRead
}

func (b *timedRecordingBus) ReadByteAt(address uint32, access BusAccess) (byte, uint32, error) {
	b.accesses = append(b.accesses, access)
	value, err := b.ReadByte(address, access.FunctionCode)
	return value, b.wait, err
}

func (b *timedRecordingBus) ReadWordAt(address uint32, access BusAccess) (uint16, uint32, error) {
	b.accesses = append(b.accesses, access)
	value, err := b.ReadWord(address, access.FunctionCode)
	return value, b.wait, err
}

func (b *timedRecordingBus) WriteByteAt(address uint32, value byte, access BusAccess) (uint32, error) {
	b.accesses = append(b.accesses, access)
	return b.wait, b.WriteByte(address, value, access.FunctionCode)
}

func (b *timedRecordingBus) WriteWordAt(address uint32, value uint16, access BusAccess) (uint32, error) {
	b.accesses = append(b.accesses, access)
	return b.wait, b.WriteWord(address, value, access.FunctionCode)
}

func TestStepAtPassesEpochAndAppliesPrefetchWait(t *testing.T) {
	bus := &timedRecordingBus{SparseMemory: SparseMemory{0x104: 0x12, 0x105: 0x34}, wait: 2}
	cpu := CPU{Bus: bus, State: State{SR: supervisor, PC: 0x104, Prefetch: [2]uint16{0x4e71, 0xabcd}}}
	result, err := cpu.StepAt(390)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bus.accesses, []BusAccess{{Clock: 390, FunctionCode: 6}}) {
		t.Fatalf("timed accesses=%+v", bus.accesses)
	}
	if result.Clocks != 6 || cpu.State.PC != 0x106 || cpu.State.Prefetch != [2]uint16{0xabcd, 0x1234} {
		t.Fatalf("result=%+v state=%+v", result, cpu.State)
	}
	wantTransaction := Transaction{Kind: "r", Cycle: 4, FC: 6, Address: 0x104, Size: 2,
		Data: 0x1234, UDS: true, LDS: true}
	wantTimeline := []BusPhase{{Cycles: 2}, {Offset: 2, Cycles: 4, Transaction: &wantTransaction}}
	if !reflect.DeepEqual(result.Transactions, []Transaction{wantTransaction}) ||
		!reflect.DeepEqual(result.Timeline, wantTimeline) {
		t.Fatalf("result=%+v want transaction=%+v timeline=%+v", result, wantTransaction, wantTimeline)
	}
}

func TestMOVEByteImmediateToAddressIndirectAppliesTimedWriteWait(t *testing.T) {
	bus := &timedRecordingBus{SparseMemory: SparseMemory{
		0x104: 0x54, 0x105: 0x88, 0x106: 0xb0, 0x107: 0xfc,
	}, wait: 4}
	cpu := CPU{Bus: bus, State: State{
		A: [7]uint32{0x200}, SR: 0x2719, PC: 0x104,
		Prefetch: [2]uint16{0x10bc, 0x0000},
	}}
	result, err := cpu.StepAt(44122)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bus.accesses, []BusAccess{{Clock: 44126, FunctionCode: 5}}) {
		t.Fatalf("timed accesses=%+v", bus.accesses)
	}
	if result.Clocks != 16 || cpu.State.PC != 0x108 ||
		cpu.State.Prefetch != [2]uint16{0x5488, 0xb0fc} || cpu.State.SR != 0x2714 ||
		bus.SparseMemory[0x200] != 0 {
		t.Fatalf("result=%+v state=%+v memory=%+v", result, cpu.State, bus.SparseMemory)
	}
	if len(result.Timeline) != 4 || result.Timeline[0].Offset != 0 ||
		result.Timeline[0].Cycles != 4 || result.Timeline[1] != (BusPhase{Offset: 4, Cycles: 4}) ||
		result.Timeline[2].Offset != 8 || result.Timeline[2].Cycles != 4 ||
		result.Timeline[3].Offset != 12 || result.Timeline[3].Cycles != 4 {
		t.Fatalf("timeline=%+v", result.Timeline)
	}
	if len(result.Transactions) != 3 || result.Transactions[1].Kind != "w" ||
		result.Transactions[1].Address != 0x200 || result.Transactions[1].FC != 5 ||
		result.Transactions[1].Size != 1 || !result.Transactions[1].UDS || result.Transactions[1].LDS {
		t.Fatalf("transactions=%+v", result.Transactions)
	}
}

func TestMOVEWordStackDisplacementToAbsoluteShortAppliesTimedWriteWait(t *testing.T) {
	bus := &timedRecordingBus{SparseMemory: SparseMemory{
		0x104: 0x02, 0x105: 0x00,
		0x106: 0x4e, 0x107: 0x71,
		0x108: 0x4e, 0x109: 0x75,
		0x206: 0x12, 0x207: 0x34,
	}, wait: 4}
	cpu := CPU{Bus: bus, State: State{
		SSP: 0x200, SR: supervisor, PC: 0x104,
		Prefetch: [2]uint16{0x31ef, 0x0006},
	}}
	result, err := cpu.StepAt(1000)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bus.accesses, []BusAccess{{Clock: 1012, FunctionCode: 5}}) {
		t.Fatalf("timed accesses=%+v", bus.accesses)
	}
	if result.Clocks != 24 || cpu.State.PC != 0x10a ||
		cpu.State.Prefetch != [2]uint16{0x4e71, 0x4e75} || bus.SparseMemory[0x200] != 0x12 ||
		bus.SparseMemory[0x201] != 0x34 {
		t.Fatalf("result=%+v state=%+v memory=%+v", result, cpu.State, bus.SparseMemory)
	}
	if len(result.Transactions) != 5 {
		t.Fatalf("transactions=%+v", result.Transactions)
	}
}

func TestMOVEWordAbsoluteShortToDataRegisterAppliesTimedReadWait(t *testing.T) {
	bus := &timedRecordingBus{SparseMemory: SparseMemory{
		0x104: 0x4e, 0x105: 0x71,
		0x106: 0x4e, 0x107: 0x75,
		0x200: 0x12, 0x201: 0x34,
	}, wait: 4, exactWordRead: 0x200}
	cpu := CPU{Bus: bus, State: State{
		SR: supervisor, PC: 0x104, Prefetch: [2]uint16{0x3038, 0x0200},
	}}
	result, err := cpu.StepAt(1000)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bus.accesses, []BusAccess{{Clock: 1004, FunctionCode: 5}}) {
		t.Fatalf("timed accesses=%+v", bus.accesses)
	}
	if result.Clocks != 16 || cpu.State.PC != 0x108 || cpu.State.D[0] != 0x1234 ||
		cpu.State.Prefetch != [2]uint16{0x4e71, 0x4e75} {
		t.Fatalf("result=%+v state=%+v", result, cpu.State)
	}
}

func (b *wordReadFaultBus) ReadWord(address uint32, fc uint8) (uint16, error) {
	if address == b.faultAddress {
		return 0, typedWordReadFault{address: address, fc: fc}
	}
	return b.SparseMemory.ReadWord(address, fc)
}

func (b *resetRecordingBus) ReadWord(address uint32, fc uint8) (uint16, error) {
	b.reads = append(b.reads, resetRead{address: address, fc: fc})
	if b.failAddress != 0 && address == b.failAddress {
		return 0, fmt.Errorf("forced reset read failure at 0x%x", address)
	}
	return b.SparseMemory.ReadWord(address, fc)
}

func (b *resetRecordingBus) M68KReset() error {
	b.externalResets++
	return b.resetErr
}

func TestResetReadsVectorsAndPrefetchWithSupervisorProgramFC(t *testing.T) {
	memory := SparseMemory{
		0: 0x00, 1: 0x01, 2: 0x00, 3: 0x00,
		4: 0x00, 5: 0xfc, 6: 0x00, 7: 0x30,
		0xfc0030: 0x60, 0xfc0031: 0x00, 0xfc0032: 0x00, 0xfc0033: 0x1c,
	}
	bus := &resetRecordingBus{SparseMemory: memory}
	cpu := CPU{Bus: bus, State: State{D: [8]uint32{0x12345678}, A: [7]uint32{0x87654321},
		USP: 0xabcdef, SSP: 1, SR: 2, PC: 3, Prefetch: [2]uint16{4, 5}}}
	if err := cpu.Reset(); err != nil {
		t.Fatal(err)
	}
	wantReads := []resetRead{{0, 6}, {2, 6}, {4, 6}, {6, 6}, {0xfc0030, 6}, {0xfc0032, 6}}
	if !reflect.DeepEqual(bus.reads, wantReads) {
		t.Fatalf("reset reads=%+v want %+v", bus.reads, wantReads)
	}
	if cpu.State.SSP != 0x00010000 || cpu.State.SR != 0x2700 || cpu.State.PC != 0xfc0034 ||
		cpu.State.Prefetch != [2]uint16{0x6000, 0x001c} || cpu.State.D[0] != 0x12345678 ||
		cpu.State.A[0] != 0x87654321 || cpu.State.USP != 0xabcdef {
		t.Fatalf("reset state=%+v", cpu.State)
	}
}

func TestResetFailureDoesNotCommitStagedState(t *testing.T) {
	memory := SparseMemory{
		0: 0x00, 1: 0x01, 2: 0x00, 3: 0x00,
		4: 0x00, 5: 0xfc, 6: 0x00, 7: 0x30,
		0xfc0030: 0x60, 0xfc0031: 0x00, 0xfc0032: 0x00, 0xfc0033: 0x1c,
	}
	initial := State{D: [8]uint32{1}, A: [7]uint32{2}, USP: 3, SSP: 4,
		SR: 5, PC: 6, Prefetch: [2]uint16{7, 8}}
	cpu := CPU{Bus: &resetRecordingBus{SparseMemory: memory, failAddress: 0xfc0032}, State: initial}
	if err := cpu.Reset(); err == nil {
		t.Fatal("reset unexpectedly succeeded")
	}
	if cpu.State != initial {
		t.Fatalf("failed reset committed state: %+v", cpu.State)
	}

	odd := SparseMemory{0: 0, 1: 1, 2: 0, 3: 0, 4: 0, 5: 0xfc, 6: 0, 7: 0x31}
	cpu = CPU{Bus: odd, State: initial}
	if err := cpu.Reset(); err == nil || cpu.State != initial {
		t.Fatalf("odd-PC reset err=%v state=%+v", err, cpu.State)
	}
	cpu = CPU{State: initial}
	if err := cpu.Reset(); err == nil || cpu.State != initial {
		t.Fatalf("nil-bus reset err=%v state=%+v", err, cpu.State)
	}
}

func TestNOPPipelineAndFunctionCode(t *testing.T) {
	memory := SparseMemory{0x1004: 0x12, 0x1005: 0x34}
	cpu := CPU{Bus: memory, State: State{
		D:  [8]uint32{1, 2, 3, 4, 5, 6, 7, 8},
		A:  [7]uint32{9, 10, 11, 12, 13, 14, 15},
		SR: 0x2000, PC: 0x1004, Prefetch: [2]uint16{0x4e71, 0xabcd},
	}}
	wantD, wantA := cpu.State.D, cpu.State.A
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.State.D != wantD || cpu.State.A != wantA {
		t.Fatal("NOP changed a data or address register")
	}
	if cpu.State.PC != 0x1006 || cpu.State.Prefetch != [2]uint16{0xabcd, 0x1234} {
		t.Fatalf("unexpected pipeline state: %#v", cpu.State)
	}
	if result.Clocks != 4 || len(result.Transactions) != 1 {
		t.Fatalf("unexpected step result: %#v", result)
	}
	want := Transaction{Kind: "r", Cycle: 4, FC: 6, Address: 0x1004, Size: 2, Data: 0x1234, UDS: true, LDS: true}
	if result.Transactions[0] != want {
		t.Fatalf("transaction = %#v, want %#v", result.Transactions[0], want)
	}
}

func TestMC68000MOVECEntersIllegalInstructionVector4(t *testing.T) {
	for _, test := range []struct {
		name      string
		opcode    uint16
		initialSR uint16
		finalSR   uint16
	}{
		{"control_to_register_supervisor_trace", 0x4e7a, 0xa704, 0x2704},
		{"register_to_control_user_trace", 0x4e7b, 0x8004, 0x2004},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory := SparseMemory{
				0x0010: 0x00, 0x0011: 0x00, 0x0012: 0x10, 0x0013: 0x00,
				0x1000: 0x21, 0x1001: 0xfc, 0x1002: 0x00, 0x1003: 0xfc,
			}
			cpu := CPU{Bus: memory, State: State{
				D: [8]uint32{0x12345678}, A: [7]uint32{0x87654321},
				USP: 0x4000, SSP: 0x3000, SR: test.initialSR, PC: 0x2004,
				Prefetch: [2]uint16{test.opcode, 0x0801},
			}}
			result, err := cpu.Step()
			if err != nil {
				t.Fatal(err)
			}
			if result.Clocks != 36 || cpu.State.SSP != 0x2ffa || cpu.State.USP != 0x4000 ||
				cpu.State.SR != test.finalSR || cpu.State.PC != 0x1004 ||
				cpu.State.Prefetch != [2]uint16{0x21fc, 0x00fc} ||
				cpu.State.D[0] != 0x12345678 || cpu.State.A[0] != 0x87654321 {
				t.Fatalf("result=%+v state=%+v", result, cpu.State)
			}
			wantFrame := []uint16{test.initialSR, 0x0000, 0x2000}
			for index, want := range wantFrame {
				got, readErr := memory.ReadWord(0x2ffa+uint32(index*2), 5)
				if readErr != nil || got != want {
					t.Fatalf("frame[%d]=%04x/%v want %04x", index, got, readErr, want)
				}
			}
			wantTransactions := []Transaction{
				writeTransaction(0x2ffe, 5, 0x2000),
				writeTransaction(0x2ffa, 5, test.initialSR),
				writeTransaction(0x2ffc, 5, 0x0000),
				readTransaction(0x0010, 5, 0x0000),
				readTransaction(0x0012, 5, 0x1000),
				readTransaction(0x1000, 6, 0x21fc),
				readTransaction(0x1002, 6, 0x00fc),
			}
			if !reflect.DeepEqual(result.Transactions, wantTransactions) {
				t.Fatalf("transactions=%+v want %+v", result.Transactions, wantTransactions)
			}
		})
	}
}

func TestMC68000RESETInstruction(t *testing.T) {
	memory := SparseMemory{0x1004: 0x12, 0x1005: 0x34}
	bus := &resetRecordingBus{SparseMemory: memory}
	initial := State{
		D: [8]uint32{0x12345678}, A: [7]uint32{0x87654321},
		USP: 0x4000, SSP: 0x3000, SR: 0x2704, PC: 0x1004,
		Prefetch: [2]uint16{0x4e70, 0xabcd},
	}
	cpu := CPU{Bus: bus, State: initial}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if bus.externalResets != 1 || result.Clocks != 132 ||
		cpu.State.D != initial.D || cpu.State.A != initial.A || cpu.State.USP != initial.USP ||
		cpu.State.SSP != initial.SSP || cpu.State.SR != initial.SR || cpu.State.PC != 0x1006 ||
		cpu.State.Prefetch != [2]uint16{0xabcd, 0x1234} {
		t.Fatalf("resets=%d result=%+v state=%+v", bus.externalResets, result, cpu.State)
	}
	wantTransactions := []Transaction{readTransaction(0x1004, 6, 0x1234)}
	if !reflect.DeepEqual(result.Transactions, wantTransactions) {
		t.Fatalf("transactions=%+v want %+v", result.Transactions, wantTransactions)
	}
}

func TestMC68000RESETSignalFailureDoesNotAdvanceCPU(t *testing.T) {
	wantErr := fmt.Errorf("forced external reset failure")
	initial := State{D: [8]uint32{1}, A: [7]uint32{2}, USP: 3, SSP: 4,
		SR: 0x2700, PC: 0x1004, Prefetch: [2]uint16{0x4e70, 0xabcd}}
	bus := &resetRecordingBus{SparseMemory: SparseMemory{}, resetErr: wantErr}
	cpu := CPU{Bus: bus, State: initial}
	result, err := cpu.Step()
	if err != wantErr || bus.externalResets != 1 || result.Clocks != 0 || result.Transactions != nil || cpu.State != initial {
		t.Fatalf("err=%v resets=%d result=%+v state=%+v", err, bus.externalResets, result, cpu.State)
	}
}

func TestMC68000RESETUserModeEntersPrivilegeViolationWithoutResetSignal(t *testing.T) {
	memory := SparseMemory{
		0x0020: 0x00, 0x0021: 0x00, 0x0022: 0x20, 0x0023: 0x00,
		0x2000: 0x4e, 0x2001: 0x71, 0x2002: 0x4e, 0x2003: 0x71,
	}
	bus := &resetRecordingBus{SparseMemory: memory}
	cpu := CPU{Bus: bus, State: State{
		USP: 0x4000, SSP: 0x3000, SR: 0x0004, PC: 0x1004,
		Prefetch: [2]uint16{0x4e70, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if bus.externalResets != 0 || result.Clocks != 34 || cpu.State.SSP != 0x2ffa ||
		cpu.State.USP != 0x4000 || cpu.State.SR != 0x2004 || cpu.State.PC != 0x2004 ||
		cpu.State.Prefetch != [2]uint16{0x4e71, 0x4e71} {
		t.Fatalf("resets=%d result=%+v state=%+v", bus.externalResets, result, cpu.State)
	}
	wantFrame := []uint16{0x0004, 0x0000, 0x1000}
	for index, want := range wantFrame {
		got, readErr := memory.ReadWord(0x2ffa+uint32(index*2), 5)
		if readErr != nil || got != want {
			t.Fatalf("frame[%d]=%04x/%v want %04x", index, got, readErr, want)
		}
	}
}

func TestAbsoluteShortWordBusErrorPreservesSignExtendedFaultAddress(t *testing.T) {
	memory := SparseMemory{
		0x0008: 0x00, 0x0009: 0x00, 0x000a: 0x20, 0x000b: 0x00,
		0x1004: 0x4e, 0x1005: 0x71,
		0x2000: 0x4e, 0x2001: 0x70, 0x2002: 0x0c, 0x2003: 0xb9,
	}
	bus := &wordReadFaultBus{SparseMemory: memory, faultAddress: 0x00ff8006}
	cpu := CPU{Bus: bus, State: State{
		SSP: 0x3000, SR: 0x2700, PC: 0x1004,
		Prefetch: [2]uint16{0x4a78, 0x8006},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if result.Clocks != 68 || cpu.State.SSP != 0x2ff2 || cpu.State.SR != 0x2700 ||
		cpu.State.PC != 0x2004 || cpu.State.Prefetch != [2]uint16{0x4e70, 0x0cb9} {
		t.Fatalf("result=%+v state=%+v", result, cpu.State)
	}
	wantFrame := []uint16{0x4a75, 0xffff, 0x8006, 0x4a78, 0x2700, 0x0000, 0x1004}
	for index, want := range wantFrame {
		got, readErr := memory.ReadWord(0x2ff2+uint32(index*2), 5)
		if readErr != nil || got != want {
			t.Fatalf("frame[%d]=%04x/%v want %04x", index, got, readErr, want)
		}
	}
	wantTransactions := []Transaction{
		readTransaction(0x1004, 6, 0x4e71),
		{Kind: "re", Cycle: 4, FC: 5, Address: 0x00ff8006, Size: 2, UDS: true, LDS: true},
		writeTransaction(0x2ffe, 5, 0x1004),
		writeTransaction(0x2ffa, 5, 0x2700),
		writeTransaction(0x2ffc, 5, 0x0000),
		writeTransaction(0x2ff8, 5, 0x4a78),
		writeTransaction(0x2ff6, 5, 0x8006),
		writeTransaction(0x2ff2, 5, 0x4a75),
		writeTransaction(0x2ff4, 5, 0xffff),
		readTransaction(0x0008, 5, 0x0000),
		readTransaction(0x000a, 5, 0x2000),
		readTransaction(0x2000, 6, 0x4e70),
		readTransaction(0x2002, 6, 0x0cb9),
	}
	if !reflect.DeepEqual(result.Transactions, wantTransactions) {
		t.Fatalf("transactions=%+v want %+v", result.Transactions, wantTransactions)
	}
}

func TestStepFailsClosed(t *testing.T) {
	for _, test := range []CPU{
		{State: State{Prefetch: [2]uint16{0x4e71}}},
		{Bus: SparseMemory{}, State: State{Prefetch: [2]uint16{0x4e72}}},
		{Bus: SparseMemory{}, State: State{Prefetch: [2]uint16{0x4ec0}}},
	} {
		if _, err := test.Step(); err == nil {
			t.Fatal("Step unexpectedly succeeded")
		}
	}
}

func TestBitOperationsRejectIllegalDestinationWithoutConsumingExtension(t *testing.T) {
	for _, opcode := range []uint16{0x08c8, 0x08fa} {
		initial := State{D: [8]uint32{1}, SR: 0x2004, PC: 0x1004,
			Prefetch: [2]uint16{opcode, 0x001f}}
		cpu := CPU{Bus: SparseMemory{0x1004: 0x4e, 0x1005: 0x71}, State: initial}
		if _, err := cpu.Step(); err == nil {
			t.Fatalf("opcode %04x unexpectedly accepted", opcode)
		}
		if cpu.State != initial {
			t.Fatalf("opcode %04x changed state on rejection: %+v", opcode, cpu.State)
		}
	}
}

func TestTASRejectsIllegalDestinationWithoutConsumingExtension(t *testing.T) {
	for _, opcode := range []uint16{0x4ac8, 0x4afa, 0x4afc} {
		initial := State{D: [8]uint32{1}, SR: 0x2004, PC: 0x1004,
			Prefetch: [2]uint16{opcode, 0x001f}}
		cpu := CPU{Bus: SparseMemory{0x1004: 0x4e, 0x1005: 0x71}, State: initial}
		if _, err := cpu.Step(); err == nil {
			t.Fatalf("opcode %04x unexpectedly accepted", opcode)
		}
		if cpu.State != initial {
			t.Fatalf("opcode %04x changed state on rejection: %+v", opcode, cpu.State)
		}
	}
}

func TestUNLKRestoresFrameAndActiveStack(t *testing.T) {
	memory := SparseMemory{
		0x8000: 0x12, 0x8001: 0x34, 0x8002: 0x56, 0x8003: 0x78,
		0x1004: 0x4e, 0x1005: 0x71,
	}
	cpu := CPU{Bus: memory, State: State{
		A: [7]uint32{0x8000}, USP: 0x9000, SSP: 0xa000, SR: 0x001f,
		PC: 0x1004, Prefetch: [2]uint16{0x4e58, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.State.A[0] != 0x12345678 || cpu.State.USP != 0x8004 || cpu.State.SSP != 0xa000 || cpu.State.SR != 0x001f {
		t.Fatalf("unexpected UNLK state: %#v", cpu.State)
	}
	want := []Transaction{
		{Kind: "r", Cycle: 4, FC: 1, Address: 0x8000, Size: 2, Data: 0x1234, UDS: true, LDS: true},
		{Kind: "r", Cycle: 4, FC: 1, Address: 0x8002, Size: 2, Data: 0x5678, UDS: true, LDS: true},
		{Kind: "r", Cycle: 4, FC: 2, Address: 0x1004, Size: 2, Data: 0x4e71, UDS: true, LDS: true},
	}
	if result.Clocks != 12 || !reflect.DeepEqual(result.Transactions, want) {
		t.Fatalf("unexpected UNLK bus result: %#v", result)
	}
}

func TestMOVEBytePostIncrementA7AndBusLane(t *testing.T) {
	memory := SparseMemory{
		0x1004: 0x12, 0x1005: 0x34,
		0x8001: 0x80,
	}
	cpu := CPU{Bus: memory, State: State{
		D: [8]uint32{0x12345678}, USP: 0x8001,
		SR: 0x0011, PC: 0x1004, Prefetch: [2]uint16{0x101f, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.State.D[0] != 0x12345680 || cpu.State.USP != 0x8003 || cpu.State.SR != 0x0018 {
		t.Fatalf("unexpected MOVE.B state: %#v", cpu.State)
	}
	want := Transaction{Kind: "r", Cycle: 4, FC: 1, Address: 0x8000, Size: 1,
		Data: 0x0080, LDS: true}
	if result.Clocks != 8 || len(result.Transactions) != 2 || result.Transactions[0] != want {
		t.Fatalf("unexpected MOVE.B bus result: %#v", result)
	}
}

func TestMOVEBytePostIncrementSourceAndDestinationAlias(t *testing.T) {
	memory := SparseMemory{
		0x1004: 0x4e, 0x1005: 0x71,
		0x8000: 0x12, 0x8001: 0xff,
	}
	cpu := CPU{Bus: memory, State: State{
		A: [7]uint32{0x8000}, SR: 0x0011,
		PC: 0x1004, Prefetch: [2]uint16{0x10d8, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.State.A[0] != 0x8002 || memory[0x8001] != 0x12 || cpu.State.SR != 0x0010 {
		t.Fatalf("unexpected aliased MOVE.B state: %#v RAM=%#v", cpu.State, memory)
	}
	want := []Transaction{
		{Kind: "r", Cycle: 4, FC: 1, Address: 0x8000, Size: 1, Data: 0x1200, UDS: true},
		{Kind: "w", Cycle: 4, FC: 1, Address: 0x8000, Size: 1, Data: 0x0012, LDS: true},
		{Kind: "r", Cycle: 4, FC: 2, Address: 0x1004, Size: 2, Data: 0x4e71, UDS: true, LDS: true},
	}
	if result.Clocks != 12 || !reflect.DeepEqual(result.Transactions, want) {
		t.Fatalf("unexpected aliased MOVE.B bus result: %#v", result)
	}
}

func TestADDAWordSignExtendsAndPreservesSR(t *testing.T) {
	memory := SparseMemory{
		0x1004: 0x4e, 0x1005: 0x71,
		0x1006: 0x70, 0x1007: 0x01,
	}
	cpu := CPU{Bus: memory, State: State{
		A: [7]uint32{0x100}, SR: 0x001f,
		PC: 0x1004, Prefetch: [2]uint16{0xd0fc, 0xffff},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.State.A[0] != 0xff || cpu.State.SR != 0x001f {
		t.Fatalf("unexpected ADDA.W state: %#v", cpu.State)
	}
	if result.Clocks != 12 || cpu.State.PC != 0x1008 || cpu.State.Prefetch != [2]uint16{0x4e71, 0x7001} {
		t.Fatalf("unexpected ADDA.W pipeline: state=%#v result=%#v", cpu.State, result)
	}
}

func TestADDALongWritesActiveStackAndPreservesSR(t *testing.T) {
	memory := SparseMemory{0x1004: 0x4e, 0x1005: 0x71}
	cpu := CPU{Bus: memory, State: State{
		D: [8]uint32{2}, USP: 0x8000, SSP: 0x9000, SR: 0x001f,
		PC: 0x1004, Prefetch: [2]uint16{0xdfc0, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.State.USP != 0x8002 || cpu.State.SSP != 0x9000 || cpu.State.SR != 0x001f {
		t.Fatalf("unexpected ADDA.L state: %#v", cpu.State)
	}
	if result.Clocks != 8 {
		t.Fatalf("unexpected ADDA.L clocks: %#v", result)
	}
}

func TestANDByteDataRegistersUpdateLogicalFlags(t *testing.T) {
	memory := SparseMemory{0x1004: 0x4e, 0x1005: 0x71}
	cpu := CPU{Bus: memory, State: State{
		D: [8]uint32{0x0000000f, 0x123456f0}, SR: 0x0013,
		PC: 0x1004, Prefetch: [2]uint16{0xc200, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if cpu.State.D[1] != 0x12345600 || cpu.State.SR != 0x0014 {
		t.Fatalf("unexpected AND.B state: %#v", cpu.State)
	}
	if result.Clocks != 4 {
		t.Fatalf("unexpected AND.B clocks: %#v", result)
	}
}

func TestANDLongMemoryUsesRMWBusOrder(t *testing.T) {
	memory := SparseMemory{
		0x1004: 0x4e, 0x1005: 0x71,
		0x8000: 0xff, 0x8001: 0xff, 0x8002: 0x00, 0x8003: 0xff,
	}
	cpu := CPU{Bus: memory, State: State{
		D: [8]uint32{0x0f0f0f0f}, A: [7]uint32{0x8000}, SR: 0x0013,
		PC: 0x1004, Prefetch: [2]uint16{0xc190, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if memory[0x8000] != 0x0f || memory[0x8001] != 0x0f || memory[0x8002] != 0x00 || memory[0x8003] != 0x0f {
		t.Fatalf("unexpected AND.L memory: %#v", memory)
	}
	wantKinds := []string{"r", "r", "r", "w", "w"}
	wantAddresses := []uint32{0x8000, 0x8002, 0x1004, 0x8002, 0x8000}
	if result.Clocks != 20 || len(result.Transactions) != len(wantKinds) {
		t.Fatalf("unexpected AND.L bus result: %#v", result)
	}
	for i := range wantKinds {
		if result.Transactions[i].Kind != wantKinds[i] || result.Transactions[i].Address != wantAddresses[i] {
			t.Fatalf("transaction %d = %#v", i, result.Transactions[i])
		}
	}
}

func TestOddBranchTargetEntersAddressError(t *testing.T) {
	memory := SparseMemory{
		0x1002: 0xab, 0x1003: 0xcd,
		0x000c: 0x00, 0x000d: 0x00, 0x000e: 0x20, 0x000f: 0x00,
		0x2000: 0x4e, 0x2001: 0x71, 0x2002: 0x70, 0x2003: 0x01,
	}
	cpu := CPU{Bus: memory, State: State{
		D:   [8]uint32{1, 2, 3, 4, 5, 6, 7, 8},
		A:   [7]uint32{9, 10, 11, 12, 13, 14, 15},
		USP: 0x8000, SSP: 0x9000,
		SR: 0x8000, PC: 0x1004, Prefetch: [2]uint16{0x6001, 0xabcd},
	}}
	result, err := cpu.Step()
	if err != nil {
		t.Fatal(err)
	}
	if result.Clocks != 60 || len(result.Transactions) != 12 {
		t.Fatalf("unexpected address-error result: %#v", result)
	}
	if cpu.State.SSP != 0x8ff2 || cpu.State.USP != 0x8000 || cpu.State.SR != 0x2000 {
		t.Fatalf("unexpected exception state: %#v", cpu.State)
	}
	if cpu.State.PC != 0x2004 || cpu.State.Prefetch != [2]uint16{0x4e71, 0x7001} {
		t.Fatalf("unexpected handler pipeline: %#v", cpu.State)
	}
	frame := []uint16{0x6012, 0x0000, 0x1003, 0x6001, 0x8000, 0x0000, 0x1002}
	for i, want := range frame {
		got, err := memory.ReadWord(0x8ff2+uint32(i*2), 5)
		if err != nil || got != want {
			t.Fatalf("frame word %d = 0x%04x, %v; want 0x%04x", i, got, err, want)
		}
	}
}
