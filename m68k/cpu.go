package m68k

import (
	"errors"
	"fmt"
)

const (
	addressMask = 0x00ff_ffff
	supervisor  = 0x2000
)

var ErrStopped = errors.New("m68k: processor is stopped")

var ErrInvalidAutovectorLevel = errors.New("m68k: autovector level must be 1 through 6")

type State struct {
	D        [8]uint32
	A        [7]uint32
	USP      uint32
	SSP      uint32
	SR       uint16
	PC       uint32
	Prefetch [2]uint16
}

type Transaction struct {
	Kind    string
	Cycle   uint32
	FC      uint8
	Address uint32
	Size    uint8
	Data    uint16
	UDS     bool
	LDS     bool
}

// BusPhase preserves the instruction-local position and duration of one
// bus timeline phase. Transaction is nil for an internal/idle phase.
// Absolute machine time is the instruction epoch plus Offset.
type BusPhase struct {
	Offset      uint32
	Cycles      uint32
	Transaction *Transaction
}

type StepResult struct {
	Clocks       uint32
	Transactions []Transaction
	Timeline     []BusPhase
}

type Bus interface {
	ReadByte(address uint32, functionCode uint8) (byte, error)
	ReadWord(address uint32, functionCode uint8) (uint16, error)
	WriteByte(address uint32, value byte, functionCode uint8) error
	WriteWord(address uint32, value uint16, functionCode uint8) error
}

// BusAccess identifies when an access starts on the machine-wide clock.
type BusAccess struct {
	Clock        uint64
	FunctionCode uint8
}

// TimedBus is the cycle-aware form of Bus. Wait is the number of idle clocks
// inserted before the access. Bus remains available during incremental CPU
// migration; exact-timing machine paths must use only migrated instructions.
type TimedBus interface {
	Bus
	ReadByteAt(address uint32, access BusAccess) (value byte, wait uint32, err error)
	ReadWordAt(address uint32, access BusAccess) (value uint16, wait uint32, err error)
	WriteByteAt(address uint32, value byte, access BusAccess) (wait uint32, err error)
	WriteWordAt(address uint32, value uint16, access BusAccess) (wait uint32, err error)
}

// ExactByteWriteBus identifies addresses whose byte-write phase has been
// migrated to TimedBus. Unverified addresses keep the legacy instruction path.
type ExactByteWriteBus interface {
	TimedBus
	HasExactByteWriteTiming(address uint32) bool
}

// BusFault exposes the information the MC68000 places in a bus-error frame.
// Backends may implement it without importing this package.
type BusFault interface {
	error
	M68KBusFault() (address uint32, functionCode uint8, write bool, size uint8)
}

// ResetBus receives the external reset signal asserted by the RESET instruction.
// A bus without modeled external device state does not need to implement it.
type ResetBus interface {
	M68KReset() error
}

type CPU struct {
	State   State
	Bus     Bus
	epoch   uint64
	stopped bool
}

func (c *CPU) IsStopped() bool {
	return c.stopped
}

// AcceptAutovector accepts an already-arbitrated level 1-6 autovector interrupt.
// Level 7 has distinct edge-triggered semantics and is intentionally fail-closed.
func (c *CPU) AcceptAutovector(level uint8) (StepResult, bool, error) {
	return c.AcceptAutovectorAt(level, 0)
}

// AcceptAutovectorAt is the cycle-aware form of AcceptAutovector.
func (c *CPU) AcceptAutovectorAt(level uint8, epoch uint64) (StepResult, bool, error) {
	if c.Bus == nil {
		return StepResult{}, false, fmt.Errorf("m68k: nil bus")
	}
	if level < 1 || level > 6 {
		return StepResult{}, false, ErrInvalidAutovectorLevel
	}
	if level <= uint8(c.State.SR>>8&7) {
		return StepResult{}, false, nil
	}
	c.epoch = epoch
	savedPC := c.State.PC - 4
	if c.stopped {
		savedPC = c.State.PC
	}
	var (
		result StepResult
		err    error
	)
	if _, ok := c.Bus.(TimedBus); ok {
		result, err = c.enterTimedStandardException(24+level, savedPC, 44)
	} else {
		result, err = c.enterStandardException(24+level, savedPC, nil, 44)
	}
	if err != nil {
		return result, false, err
	}
	c.State.SR = c.State.SR&^0x0700 | uint16(level)<<8
	c.stopped = false
	return result, true, nil
}

func (c *CPU) Reset() error {
	if c.Bus == nil {
		return fmt.Errorf("m68k: nil bus")
	}
	readLong := func(address uint32) (uint32, error) {
		high, err := c.Bus.ReadWord(address&addressMask, 6)
		if err != nil {
			return 0, err
		}
		low, err := c.Bus.ReadWord((address+2)&addressMask, 6)
		if err != nil {
			return 0, err
		}
		return uint32(high)<<16 | uint32(low), nil
	}

	ssp, err := readLong(0)
	if err != nil {
		return err
	}
	initialPC, err := readLong(4)
	if err != nil {
		return err
	}
	if initialPC&1 != 0 {
		return fmt.Errorf("m68k: odd reset PC 0x%08x", initialPC)
	}
	first, err := c.Bus.ReadWord(initialPC&addressMask, 6)
	if err != nil {
		return err
	}
	second, err := c.Bus.ReadWord((initialPC+2)&addressMask, 6)
	if err != nil {
		return err
	}

	c.State.SSP = ssp
	c.State.SR = 0x2700
	c.State.PC = initialPC + 4
	c.State.Prefetch = [2]uint16{first, second}
	c.stopped = false
	return nil
}

func (c *CPU) Step() (StepResult, error) {
	return c.StepAt(0)
}

// StepAt executes one instruction at a machine-wide clock epoch. Instructions
// are migrated to timed bus access incrementally under spec 055.
func (c *CPU) StepAt(epoch uint64) (StepResult, error) {
	if c.Bus == nil {
		return StepResult{}, fmt.Errorf("m68k: nil bus")
	}
	if c.stopped {
		return StepResult{}, ErrStopped
	}
	c.epoch = epoch
	opcode := c.State.Prefetch[0]
	switch {
	case opcode&0xf000 == 0xf000:
		if _, ok := c.Bus.(TimedBus); ok {
			return c.enterTimedStandardException(11, c.State.PC-4, 34)
		}
		return c.enterStandardException(11, c.State.PC-4, nil, 34)
	case opcode&0xff00 == 0x0800:
		return c.stepBit(opcode, true)
	case opcode&0xf100 == 0x0100:
		return c.stepBit(opcode, false)
	case opcode&0xf0f8 == 0x50c8:
		return c.stepDBcc(opcode)
	case opcode&0xf0c0 == 0x50c0:
		return c.stepScc(opcode)
	case opcode&0xffc0 == 0x44c0:
		return c.stepMOVEToStatus(opcode, false)
	case opcode&0xffc0 == 0x46c0:
		return c.stepMOVEToStatus(opcode, true)
	case opcode&0xffc0 == 0x40c0:
		return c.stepMOVEFromSR(opcode)
	case opcode&0xff00 == 0x4600 && opcode>>6&3 <= 2:
		return c.stepUnaryModify(opcode, false)
	case opcode&0xff00 == 0x4400 && opcode>>6&3 <= 2:
		return c.stepUnaryModify(opcode, true)
	case opcode&0xf1f8 == 0xc140:
		return c.stepEXG(opcode, 0)
	case opcode&0xf1f8 == 0xc148:
		return c.stepEXG(opcode, 1)
	case opcode&0xf1f8 == 0xc188:
		return c.stepEXG(opcode, 2)
	case opcode&0xffc0 == 0xe0c0:
		return c.stepASRMemory(opcode)
	case opcode&0xf118 == 0xe000 && opcode>>6&3 <= 2:
		return c.stepASRRegister(opcode)
	case opcode&0xffc0 == 0xe2c0:
		return c.stepLSRMemory(opcode)
	case opcode&0xf118 == 0xe008 && opcode>>6&3 <= 2:
		return c.stepLSRRegister(opcode)
	case opcode&0xffc0 == 0xe6c0:
		return c.stepRORMemory(opcode)
	case opcode&0xf118 == 0xe018 && opcode>>6&3 <= 2:
		return c.stepRORRegister(opcode)
	case opcode&0xffc0 == 0xe7c0:
		return c.stepROLMemory(opcode)
	case opcode&0xf118 == 0xe118 && opcode>>6&3 <= 2:
		return c.stepROLRegister(opcode)
	case opcode&0xffc0 == 0xe1c0:
		return c.stepASLMemory(opcode)
	case opcode&0xf118 == 0xe100 && opcode>>6&3 <= 2:
		return c.stepASLRegister(opcode)
	case opcode&0xffc0 == 0xe3c0:
		return c.stepLSLMemory(opcode)
	case opcode&0xf118 == 0xe108 && opcode>>6&3 <= 2:
		return c.stepLSLRegister(opcode)
	case opcode&0xffc0 == 0x4ac0:
		return c.stepTAS(opcode)
	case opcode&0xff00 == 0x4a00 && opcode>>6&3 <= 2:
		return c.stepTST(opcode)
	case opcode&0xfff8 == 0x4e50:
		return c.stepLINK(opcode)
	case opcode&0xfff8 == 0x4e58:
		return c.stepUNLK(opcode)
	case opcode&0xfb80 == 0x4880 && opcode>>3&7 >= 2:
		return c.stepMOVEM(opcode)
	case opcode&0xff00 == 0x4200 && opcode>>6&3 <= 2:
		return c.stepCLR(opcode)
	case opcode&0xff00 == 0x0600 && opcode>>6&3 <= 2:
		return c.stepADDImmediate(opcode)
	case opcode&0xff00 == 0x0400 && opcode>>6&3 <= 2:
		return c.stepSUBImmediate(opcode)
	case opcode&0xf100 == 0x5000 && opcode>>6&3 <= 2:
		return c.stepADDQuick(opcode)
	case opcode&0xf100 == 0x5100 && opcode>>6&3 <= 2:
		return c.stepSUBQuick(opcode)
	case opcode&0xf000 == 0xd000 && opcode>>6&7 <= 2:
		return c.stepADDToDataRegister(opcode)
	case opcode&0xf000 == 0xd000 && opcode>>6&7 >= 4 && opcode>>6&7 <= 6 && opcode>>3&7 >= 2:
		return c.stepADDToMemory(opcode)
	case opcode&0xf000 == 0x9000 && opcode>>6&7 <= 2:
		return c.stepSUBToDataRegister(opcode)
	case opcode&0xf000 == 0x9000 && opcode>>6&7 >= 4 && opcode>>6&7 <= 6 && opcode>>3&7 >= 2:
		return c.stepSUBToMemory(opcode)
	case opcode&0xff00 == 0x0c00 && opcode>>6&3 <= 2:
		return c.stepCMPImmediate(opcode)
	case opcode&0xf1c0 == 0xb0c0:
		return c.stepCMPA(opcode, false)
	case opcode&0xf1c0 == 0xb1c0:
		return c.stepCMPA(opcode, true)
	case opcode&0xf138 == 0xb108:
		return c.stepCMPMemory(opcode)
	case opcode&0xf000 == 0xb000 && opcode>>6&7 <= 2:
		return c.stepCMP(opcode)
	case opcode&0xff00 == 0x0a00 && opcode&0x003f != 0x003c:
		return c.stepEORImmediate(opcode)
	case opcode&0xf000 == 0xb000 && opcode>>6&7 == 4:
		return c.stepEORByteToDestination(opcode)
	case opcode&0xf000 == 0xb000 && opcode>>6&7 == 5:
		return c.stepEORWordToDestination(opcode)
	case opcode&0xf000 == 0xb000 && opcode>>6&7 == 6:
		return c.stepEORLongToDestination(opcode)
	case opcode&0xff00 == 0x0200 && opcode&0x003f != 0x003c:
		return c.stepANDImmediate(opcode)
	case opcode&0xff00 == 0x0000 && opcode&0x003f != 0x003c:
		return c.stepORImmediate(opcode)
	case opcode&0xf000 == 0x8000 && opcode>>6&7 <= 2:
		return c.stepORToDataRegister(opcode)
	case opcode&0xf000 == 0x8000 && opcode>>6&7 == 4:
		return c.stepORByteToMemory(opcode)
	case opcode&0xf000 == 0x8000 && opcode>>6&7 == 5:
		return c.stepORWordToMemory(opcode)
	case opcode&0xf000 == 0x8000 && opcode>>6&7 == 6:
		return c.stepORLongToMemory(opcode)
	case opcode&0xf000 == 0xc000 && opcode>>6&7 <= 2:
		return c.stepANDToDataRegister(opcode)
	case opcode&0xf000 == 0xc000 && opcode>>6&7 == 4:
		return c.stepANDByteToMemory(opcode)
	case opcode&0xf000 == 0xc000 && opcode>>6&7 == 5:
		return c.stepANDWordToMemory(opcode)
	case opcode&0xf000 == 0xc000 && opcode>>6&7 == 6:
		return c.stepANDLongToMemory(opcode)
	case opcode&0xf1c0 == 0xc0c0:
		return c.stepMultiply(opcode, false)
	case opcode&0xf1c0 == 0xc1c0:
		return c.stepMultiply(opcode, true)
	case opcode&0xf1c0 == 0x80c0:
		return c.stepDivide(opcode, false)
	case opcode&0xf1c0 == 0x81c0:
		return c.stepDivide(opcode, true)
	case opcode&0xf1c0 == 0xd0c0:
		return c.stepADDAWord(opcode)
	case opcode&0xf1c0 == 0xd1c0:
		return c.stepADDALong(opcode)
	case opcode&0xf1c0 == 0x90c0:
		return c.stepSUBAWord(opcode)
	case opcode&0xf1c0 == 0x91c0:
		return c.stepSUBALong(opcode)
	case opcode&0xf000 == 0x2000 && opcode>>6&7 == 1:
		return c.stepMOVEALong(opcode)
	case opcode&0xf000 == 0x1000:
		return c.stepMOVEByte(opcode)
	case opcode&0xf000 == 0x2000:
		return c.stepMOVELong(opcode)
	case opcode&0xf000 == 0x3000 && opcode>>6&7 == 1:
		return c.stepMOVEAWord(opcode)
	case opcode&0xf000 == 0x3000:
		return c.stepMOVEWord(opcode)
	case opcode&0xf1c0 == 0x41c0 && isControlMode(opcode):
		return c.stepLEA(opcode)
	case opcode&0xffc0 == 0x4840 && isControlMode(opcode):
		return c.stepPEA(opcode)
	case opcode&0xffc0 == 0x4ec0:
		return c.stepJMP(opcode)
	case opcode&0xffc0 == 0x4e80:
		return c.stepJSR(opcode)
	case opcode == 0x4e75:
		return c.stepRTS(opcode)
	case opcode == 0x4e73:
		return c.stepRTE(opcode)
	case opcode == 0x4e72:
		if c.State.SR&supervisor == 0 {
			return c.enterStandardException(8, c.State.PC-4, nil, 34)
		}
		c.State.SR = c.State.Prefetch[1] & 0xa71f
		c.stopped = true
		return StepResult{Clocks: 4, Transactions: []Transaction{}}, nil
	case opcode == 0x4e70:
		if c.State.SR&supervisor == 0 {
			return c.enterStandardException(8, c.State.PC-4, nil, 34)
		}
		if bus, ok := c.Bus.(ResetBus); ok {
			if err := bus.M68KReset(); err != nil {
				return StepResult{}, err
			}
		}
		return c.refillSequential(controlEA{returnPC: c.State.PC}, 132)
	case opcode == 0x4e7a || opcode == 0x4e7b:
		return c.enterStandardException(4, c.State.PC-4, nil, 36)
	case opcode&0xfff8 == 0x4e60:
		return c.stepMOVEToUSP(opcode)
	case opcode&0xfff8 == 0x4e68:
		return c.stepMOVEFromUSP(opcode)
	case opcode&0xfff0 == 0x4e40:
		return c.enterStandardException(uint8(32+opcode&15), c.State.PC-2, nil, 34)
	case opcode&0xff00 == 0x6100:
		return c.stepBSR(opcode)
	case opcode&0xf000 == 0x6000 && opcode&0x0f00 != 0x0100:
		return c.stepBranch(opcode)
	case opcode == 0x4e71:
		// NOP changes only the prefetch pipeline.
	case opcode&0xfff8 == 0x4840:
		reg := opcode & 7
		value := c.State.D[reg]
		value = value<<16 | value>>16
		c.State.D[reg] = value
		c.setLogicalFlags(value, 32)
	case opcode&0xfff8 == 0x4880:
		reg := opcode & 7
		value := c.State.D[reg]
		word := uint32(uint16(int16(int8(value))))
		c.State.D[reg] = value&0xffff_0000 | word
		c.setLogicalFlags(word, 16)
	case opcode&0xfff8 == 0x48c0:
		reg := opcode & 7
		value := uint32(int32(int16(c.State.D[reg])))
		c.State.D[reg] = value
		c.setLogicalFlags(value, 32)
	case opcode&0xf100 == 0x7000:
		reg := (opcode >> 9) & 7
		value := uint32(int32(int8(opcode)))
		c.State.D[reg] = value
		c.State.SR &^= 0x000f
		if value == 0 {
			c.State.SR |= 0x0004
		}
		if value&0x8000_0000 != 0 {
			c.State.SR |= 0x0008
		}
	default:
		return StepResult{}, fmt.Errorf("m68k: opcode 0x%04x is not implemented", c.State.Prefetch[0])
	}

	address := c.State.PC & addressMask
	fc := uint8(2)
	if c.State.SR&supervisor != 0 {
		fc = 6
	}
	word, wait, err := c.readWordAt(address, fc, 0)
	if err != nil {
		return StepResult{}, err
	}
	c.State.Prefetch[0] = c.State.Prefetch[1]
	c.State.Prefetch[1] = word
	c.State.PC += 2

	transaction := Transaction{
		Kind: "r", Cycle: 4, FC: fc, Address: address, Size: 2,
		Data: word, UDS: true, LDS: true,
	}
	timeline := make([]BusPhase, 0, 2)
	if wait != 0 {
		timeline = append(timeline, BusPhase{Cycles: wait})
	}
	timeline = append(timeline, BusPhase{Offset: wait, Cycles: 4, Transaction: &transaction})
	return StepResult{Clocks: 4 + wait, Transactions: []Transaction{transaction}, Timeline: timeline}, nil
}

func (c *CPU) readWordAt(address uint32, functionCode uint8, offset uint32) (uint16, uint32, error) {
	if bus, ok := c.Bus.(TimedBus); ok {
		return bus.ReadWordAt(address, BusAccess{Clock: c.epoch + uint64(offset), FunctionCode: functionCode})
	}
	value, err := c.Bus.ReadWord(address, functionCode)
	return value, 0, err
}

func (c *CPU) writeWordAt(address uint32, value uint16, functionCode uint8, offset uint32) (uint32, error) {
	if bus, ok := c.Bus.(TimedBus); ok {
		return bus.WriteWordAt(address, value, BusAccess{Clock: c.epoch + uint64(offset), FunctionCode: functionCode})
	}
	return 0, c.Bus.WriteWord(address, value, functionCode)
}

func (c *CPU) stepMOVEToUSP(opcode uint16) (StepResult, error) {
	if c.State.SR&supervisor == 0 {
		return c.enterStandardException(8, c.State.PC-4, nil, 34)
	}
	c.State.USP = c.addressRegister(uint8(opcode & 7))
	return c.refillSequential(controlEA{returnPC: c.State.PC}, 4)
}

func (c *CPU) stepMOVEFromUSP(opcode uint16) (StepResult, error) {
	if c.State.SR&supervisor == 0 {
		return c.enterStandardException(8, c.State.PC-4, nil, 34)
	}
	c.setAddressRegister(uint8(opcode&7), c.State.USP)
	return c.refillSequential(controlEA{returnPC: c.State.PC}, 4)
}

func (c *CPU) stepMOVEToStatus(opcode uint16, statusRegister bool) (StepResult, error) {
	if statusRegister && c.State.SR&supervisor == 0 {
		return c.enterStandardException(8, c.State.PC-4, nil, 34)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	source, cost, _, fault, err := step.readWordSource(uint8(opcode>>3&7), uint8(opcode&7))
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	if statusRegister {
		c.State.SR = source & 0xa71f
		step.programFC = c.programFunctionCode()
	} else {
		c.State.SR = c.State.SR&0xff00 | source&0x001f
	}
	step.programFC = c.programFunctionCode()
	pipelineAddress := (c.State.PC - 2) & addressMask
	pipelineWord, err := c.Bus.ReadWord(pipelineAddress, step.programFC)
	if err != nil {
		return StepResult{}, err
	}
	step.transactions = append(step.transactions, readTransaction(pipelineAddress, step.programFC, pipelineWord))
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 12 + cost, Transactions: step.transactions}, nil
}

func (c *CPU) stepMOVEFromSR(opcode uint16) (StepResult, error) {
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid MOVE from SR destination mode %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	if mode == 0 {
		c.State.D[reg] = c.State.D[reg]&0xffff_0000 | uint32(c.State.SR)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 6, Transactions: stream.transactions}, nil
	}
	return stream.logicalWordMemory(opcode, mode, reg, c.State.SR, 8, logicalReplace)
}

func (c *CPU) stepBit(opcode uint16, immediate bool) (StepResult, error) {
	operation := uint8(opcode >> 6 & 3)
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && (reg > 4 || operation != 0 && reg > 1) {
		return StepResult{}, fmt.Errorf("m68k: invalid bit destination mode %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}

	var bitNumber uint32
	if immediate {
		extension, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		bitNumber = uint32(extension)
	} else {
		bitNumber = c.State.D[opcode>>9&7]
	}

	if mode == 0 {
		bit := bitNumber & 31
		mask := uint32(1) << bit
		value := c.State.D[reg]
		c.setBitZero(value&mask == 0)
		switch operation {
		case 1:
			c.State.D[reg] = value ^ mask
		case 2:
			c.State.D[reg] = value &^ mask
		case 3:
			c.State.D[reg] = value | mask
		}
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		clocks := uint32(6)
		if operation == 2 {
			clocks = 8
		}
		if immediate {
			clocks += 4
		}
		if operation != 0 && bit >= 16 {
			clocks += 2
		}
		return StepResult{Clocks: clocks, Transactions: stream.transactions}, nil
	}

	value, address, eaCost, memory, err := stream.readBitOperand(mode, reg)
	if err != nil {
		return StepResult{}, err
	}
	bit := uint8(bitNumber & 7)
	mask := byte(1 << bit)
	c.setBitZero(value&mask == 0)
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	clocks := uint32(4) + eaCost
	if immediate {
		clocks += 4
	}
	if operation == 0 {
		if !memory {
			clocks = 10
		}
		return StepResult{Clocks: clocks, Transactions: stream.transactions}, nil
	}
	switch operation {
	case 1:
		value ^= mask
	case 2:
		value &^= mask
	case 3:
		value |= mask
	}
	if err := stream.writeByte(address, value); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: clocks + 4, Transactions: stream.transactions}, nil
}

func (c *CPU) setBitZero(clear bool) {
	c.State.SR &^= 0x0004
	if clear {
		c.State.SR |= 0x0004
	}
}

func (s *moveByteStep) readBitOperand(mode, reg uint8) (byte, uint32, uint32, bool, error) {
	c := s.cpu
	var address, cost uint32
	readFC := s.dataFC
	switch mode {
	case 2:
		address, cost = c.addressRegister(reg), 4
	case 3:
		address, cost = c.addressRegister(reg), 4
	case 4:
		address = c.addressRegister(reg) - byteAddressDelta(reg)
		c.setAddressRegister(reg, address)
		cost = 6
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, 0, false, err
		}
		address, cost = c.addressRegister(reg)+uint32(int32(int16(extension))), 8
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, 0, false, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return 0, 0, 0, false, err
		}
		address, cost = c.addressRegister(reg)+index+uint32(int32(int8(extension))), 10
	case 7:
		switch reg {
		case 0:
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, 0, false, err
			}
			address, cost = uint32(int32(int16(extension))), 8
		case 1:
			high, err := s.consumeExtension()
			if err != nil {
				return 0, 0, 0, false, err
			}
			low, err := s.consumeExtension()
			if err != nil {
				return 0, 0, 0, false, err
			}
			address, cost = uint32(high)<<16|uint32(low), 12
		case 2, 3:
			base := c.State.PC - 2
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, 0, false, err
			}
			address = base + uint32(int32(int16(extension)))
			cost, readFC = 8, s.programFC
			if reg == 3 {
				index, indexErr := c.briefIndex(extension)
				if indexErr != nil {
					return 0, 0, 0, false, indexErr
				}
				address = base + index + uint32(int32(int8(extension)))
				cost = 10
			}
		case 4:
			extension, err := s.consumeExtension()
			return byte(extension), 0, 4, false, err
		default:
			return 0, 0, 0, false, fmt.Errorf("m68k: invalid bit operand mode %d:%d", mode, reg)
		}
	default:
		return 0, 0, 0, false, fmt.Errorf("m68k: invalid bit operand mode %d:%d", mode, reg)
	}
	value, err := c.Bus.ReadByte(address&addressMask, readFC)
	if err != nil {
		return 0, 0, 0, true, err
	}
	s.transactions = append(s.transactions, readByteTransaction(address&addressMask, readFC, value))
	if mode == 3 {
		c.setAddressRegister(reg, c.addressRegister(reg)+byteAddressDelta(reg))
	}
	return value, address & addressMask, cost, true, nil
}

func (c *CPU) stepDBcc(opcode uint16) (StepResult, error) {
	condition := uint8(opcode >> 8 & 15)
	if condition == 0 || condition != 1 && branchCondition(condition, c.State.SR) {
		return c.refillBranch(c.State.PC, 12)
	}
	reg := uint8(opcode & 7)
	result := uint16(c.State.D[reg]) - 1
	if result == 0xffff {
		c.State.D[reg] = c.State.D[reg]&0xffff_0000 | uint32(result)
		return c.refillBranch(c.State.PC, 14)
	}
	base := c.State.PC - 2
	target := uint32(int32(base) + int32(int16(c.State.Prefetch[1])))
	if target&1 != 0 {
		return c.enterInstructionAddressError(opcode, target, c.State.PC, nil, 60)
	}
	c.State.D[reg] = c.State.D[reg]&0xffff_0000 | uint32(result)
	return c.refillBranch(target, 10)
}

func (c *CPU) stepScc(opcode uint16) (StepResult, error) {
	condition := uint8(opcode >> 8 & 15)
	trueCondition := condition == 0 || condition != 1 && branchCondition(condition, c.State.SR)
	value := byte(0)
	if trueCondition {
		value = 0xff
	}
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid Scc destination mode %d:%d", mode, reg)
	}
	if mode == 0 {
		c.State.D[reg] = c.State.D[reg]&0xffff_ff00 | uint32(value)
		clocks := uint32(4)
		if trueCondition {
			clocks = 6
		}
		return c.refillSequential(controlEA{returnPC: c.State.PC}, clocks)
	}

	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	var address, delta, cost uint32
	switch mode {
	case 2:
		address, cost = c.addressRegister(reg), 4
	case 3:
		address, delta, cost = c.addressRegister(reg), byteAddressDelta(reg), 4
	case 4:
		delta = byteAddressDelta(reg)
		address, cost = c.addressRegister(reg)-delta, 6
		c.setAddressRegister(reg, address)
	case 5:
		extension, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		address, cost = c.addressRegister(reg)+uint32(int32(int16(extension))), 8
	case 6:
		extension, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return StepResult{}, err
		}
		address, cost = c.addressRegister(reg)+index+uint32(int32(int8(extension))), 10
	case 7:
		if reg == 0 {
			extension, err := stream.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			address, cost = uint32(int32(int16(extension))), 8
		} else {
			high, err := stream.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			low, err := stream.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			address, cost = uint32(high)<<16|uint32(low), 12
		}
	}
	readValue, err := c.Bus.ReadByte(address&addressMask, stream.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, readByteTransaction(address&addressMask, stream.dataFC, readValue))
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	if err := stream.writeByte(address, value); err != nil {
		return StepResult{}, err
	}
	if mode == 3 {
		c.setAddressRegister(reg, c.addressRegister(reg)+delta)
	}
	return StepResult{Clocks: 8 + cost, Transactions: stream.transactions}, nil
}

func (c *CPU) stepTAS(opcode uint16) (StepResult, error) {
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid TAS destination mode %d:%d", mode, reg)
	}
	if mode == 0 {
		value := byte(c.State.D[reg])
		c.setLogicalFlags(uint32(value), 8)
		c.State.D[reg] |= 0x80
		return c.refillSequential(controlEA{returnPC: c.State.PC}, 4)
	}

	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	value, address, cost, _, err := stream.readBitOperand(mode, reg)
	if err != nil {
		return StepResult{}, err
	}
	c.setLogicalFlags(uint32(value), 8)
	if err := stream.writeByte(address, value|0x80); err != nil {
		return StepResult{}, err
	}
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 12 + cost, Transactions: stream.transactions}, nil
}

func (c *CPU) stepUnaryModify(opcode uint16, negate bool) (StepResult, error) {
	size, mode, reg := uint8(opcode>>6&3), uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid unary destination mode %d:%d", mode, reg)
	}
	if mode == 0 {
		value := c.State.D[reg]
		var result uint32
		switch size {
		case 0:
			result = c.applyUnary(uint32(byte(value)), 8, negate)
			c.State.D[reg] = value&0xffff_ff00 | result
		case 1:
			result = c.applyUnary(uint32(uint16(value)), 16, negate)
			c.State.D[reg] = value&0xffff_0000 | result
		case 2:
			result = c.applyUnary(value, 32, negate)
			c.State.D[reg] = result
		default:
			return StepResult{}, fmt.Errorf("m68k: invalid unary size %d", size)
		}
		clocks := uint32(4)
		if size == 2 {
			clocks = 6
		}
		return c.refillSequential(controlEA{returnPC: c.State.PC}, clocks)
	}
	return c.stepUnaryMemory(opcode, size, mode, reg, negate)
}

func (c *CPU) stepUnaryMemory(opcode uint16, size, mode, reg uint8, negate bool) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	if size == 0 {
		var address, delta, cost uint32
		switch mode {
		case 2:
			address, cost = c.addressRegister(reg), 4
		case 3:
			address, delta, cost = c.addressRegister(reg), byteAddressDelta(reg), 4
		case 4:
			delta = byteAddressDelta(reg)
			address, cost = c.addressRegister(reg)-delta, 6
			c.setAddressRegister(reg, address)
		case 5:
			extension, err := stream.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			address, cost = c.addressRegister(reg)+uint32(int32(int16(extension))), 8
		case 6:
			extension, err := stream.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			index, err := c.briefIndex(extension)
			if err != nil {
				return StepResult{}, err
			}
			address, cost = c.addressRegister(reg)+index+uint32(int32(int8(extension))), 10
		case 7:
			if reg == 0 {
				extension, err := stream.consumeExtension()
				if err != nil {
					return StepResult{}, err
				}
				address, cost = uint32(int32(int16(extension))), 8
			} else {
				high, err := stream.consumeExtension()
				if err != nil {
					return StepResult{}, err
				}
				low, err := stream.consumeExtension()
				if err != nil {
					return StepResult{}, err
				}
				address, cost = uint32(high)<<16|uint32(low), 12
			}
		}
		value, err := c.Bus.ReadByte(address&addressMask, stream.dataFC)
		if err != nil {
			return StepResult{}, err
		}
		stream.transactions = append(stream.transactions, readByteTransaction(address&addressMask, stream.dataFC, value))
		result := byte(c.applyUnary(uint32(value), 8, negate))
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		if err := stream.writeByte(address, result); err != nil {
			return StepResult{}, err
		}
		if mode == 3 {
			c.setAddressRegister(reg, c.addressRegister(reg)+delta)
		}
		return StepResult{Clocks: 8 + cost, Transactions: stream.transactions}, nil
	}

	initialPC := c.State.PC
	bytes := uint32(2)
	base := uint32(8)
	if size == 2 {
		bytes, base = 4, 12
	}
	address, cost, err := stream.andMemoryAddress(mode, reg, bytes)
	if err != nil {
		return StepResult{}, err
	}
	if address&1 != 0 {
		if size == 1 {
			step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: initialPC}
			return c.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), stream.transactions, 54+cost, stream.dataFC, "re", true)
		}
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: initialPC}
		return c.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), stream.transactions, 50+cost, stream.dataFC, "re", true)
	}
	if size == 1 {
		value, err := c.Bus.ReadWord(address&addressMask, stream.dataFC)
		if err != nil {
			return StepResult{}, err
		}
		stream.transactions = append(stream.transactions, readTransaction(address&addressMask, stream.dataFC, value))
		result := uint16(c.applyUnary(uint32(value), 16, negate))
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		if err := c.Bus.WriteWord(address&addressMask, result, stream.dataFC); err != nil {
			return StepResult{}, err
		}
		stream.transactions = append(stream.transactions, writeTransaction(address&addressMask, stream.dataFC, result))
		return StepResult{Clocks: base + cost, Transactions: stream.transactions}, nil
	}
	if mode == 3 {
		c.setAddressRegister(reg, c.addressRegister(reg)+4)
	}
	high, err := c.Bus.ReadWord(address&addressMask, stream.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	low, err := c.Bus.ReadWord((address+2)&addressMask, stream.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, readTransaction(address&addressMask, stream.dataFC, high), readTransaction((address+2)&addressMask, stream.dataFC, low))
	result := c.applyUnary(uint32(high)<<16|uint32(low), 32, negate)
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	if err := c.Bus.WriteWord((address+2)&addressMask, uint16(result), stream.dataFC); err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, writeTransaction((address+2)&addressMask, stream.dataFC, uint16(result)))
	if err := c.Bus.WriteWord(address&addressMask, uint16(result>>16), stream.dataFC); err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, writeTransaction(address&addressMask, stream.dataFC, uint16(result>>16)))
	return StepResult{Clocks: base + cost, Transactions: stream.transactions}, nil
}

func (c *CPU) applyUnary(value uint32, bits uint8, negate bool) uint32 {
	var mask uint32
	switch bits {
	case 8:
		mask = 0xff
	case 16:
		mask = 0xffff
	case 32:
		mask = 0xffff_ffff
	default:
		panic("m68k: invalid unary width")
	}
	value &= mask
	if negate {
		result := -value & mask
		c.setArithmeticFlags(0, value, result, bits, true)
		return result
	}
	result := ^value & mask
	c.setLogicalFlags(result, bits)
	return result
}

func (c *CPU) stepMultiply(opcode uint16, signed bool) (StepResult, error) {
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 4 {
		return StepResult{}, fmt.Errorf("m68k: invalid multiply source mode %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	source, cost, _, fault, err := step.readWordSource(mode, reg)
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	destination := uint16(c.State.D[opcode>>9&7])
	var result uint32
	var clocks uint32
	if signed {
		result = uint32(int32(int16(destination)) * int32(int16(source)))
		clocks = multiplySignedClocks(source)
	} else {
		result = uint32(destination) * uint32(source)
		clocks = multiplyUnsignedClocks(source)
	}
	c.State.D[opcode>>9&7] = result
	c.setLogicalFlags(result, 32)
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: clocks + cost, Transactions: step.transactions}, nil
}

func multiplyUnsignedClocks(multiplier uint16) uint32 {
	clocks := uint32(38)
	for value := multiplier; value != 0; value >>= 1 {
		clocks += 2 * uint32(value&1)
	}
	return clocks
}

func multiplySignedClocks(multiplier uint16) uint32 {
	clocks := uint32(38)
	previous := uint16(0)
	for bit := uint8(0); bit < 16; bit++ {
		current := multiplier >> bit & 1
		if current != previous {
			clocks += 2
		}
		previous = current
	}
	return clocks
}

func (c *CPU) stepDivide(opcode uint16, signed bool) (StepResult, error) {
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 4 {
		return StepResult{}, fmt.Errorf("m68k: invalid divide source mode %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	divisor, cost, _, fault, err := step.readWordSource(mode, reg)
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	if divisor == 0 {
		c.State.SR &^= 0x000f
		c.State.SR |= 0x0004
		return c.enterStandardException(5, c.State.PC-2, step.transactions, 40+cost)
	}
	destination := opcode >> 9 & 7
	dividend := c.State.D[destination]
	var result uint32
	var clocks uint32
	var overflow bool
	if signed {
		quotient := int64(int32(dividend)) / int64(int16(divisor))
		remainder := int64(int32(dividend)) % int64(int16(divisor))
		overflow = quotient < -32768 || quotient > 32767
		clocks = divideSignedClocks(dividend, divisor)
		if !overflow {
			result = uint32(uint16(int16(remainder)))<<16 | uint32(uint16(int16(quotient)))
		}
	} else {
		quotient := uint64(dividend) / uint64(divisor)
		remainder := uint64(dividend) % uint64(divisor)
		overflow = quotient > 0xffff
		clocks = divideUnsignedClocks(dividend, divisor)
		if !overflow {
			result = uint32(remainder)<<16 | uint32(quotient)
		}
	}
	if overflow {
		c.State.SR &^= 0x000f
		c.State.SR |= 0x000a
	} else {
		c.State.D[destination] = result
		c.setLogicalFlags(uint32(uint16(result)), 16)
	}
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: clocks + cost, Transactions: step.transactions}, nil
}

func (c *CPU) enterStandardException(vector uint8, savedPC uint32, prefix []Transaction, clocks uint32) (StepResult, error) {
	originalSR := c.State.SR
	newSP := c.State.SSP - 6
	writes := []struct {
		address uint32
		value   uint16
	}{
		{newSP + 4, uint16(savedPC)},
		{newSP, originalSR},
		{newSP + 2, uint16(savedPC >> 16)},
	}
	transactions := append([]Transaction{}, prefix...)
	for _, write := range writes {
		address := write.address & addressMask
		if err := c.Bus.WriteWord(address, write.value, 5); err != nil {
			return StepResult{}, err
		}
		transactions = append(transactions, writeTransaction(address, 5, write.value))
	}
	vectorAddress := uint32(vector) * 4
	high, err := c.Bus.ReadWord(vectorAddress, 5)
	if err != nil {
		return StepResult{}, err
	}
	low, err := c.Bus.ReadWord(vectorAddress+2, 5)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions,
		readTransaction(vectorAddress, 5, high),
		readTransaction(vectorAddress+2, 5, low))
	handler := uint32(high)<<16 | uint32(low)
	first, err := c.Bus.ReadWord(handler&addressMask, 6)
	if err != nil {
		return StepResult{}, err
	}
	second, err := c.Bus.ReadWord((handler+2)&addressMask, 6)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions,
		readTransaction(handler&addressMask, 6, first),
		readTransaction((handler+2)&addressMask, 6, second))
	c.State.SSP = newSP
	c.State.SR = originalSR | supervisor
	c.State.SR &^= 0x8000
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = handler + 4
	return StepResult{Clocks: clocks, Transactions: transactions}, nil
}

func (c *CPU) enterTimedStandardException(vector uint8, savedPC uint32, clocks uint32) (StepResult, error) {
	originalSR := c.State.SR
	newSP := c.State.SSP - 6
	transactions := make([]Transaction, 0, 7)
	timeline := []BusPhase{{Cycles: clocks - 28}}
	offset := clocks - 28

	appendWait := func(wait uint32) {
		if wait != 0 {
			timeline = append(timeline, BusPhase{Offset: offset, Cycles: wait})
			offset += wait
		}
	}
	writeWord := func(address uint32, value uint16, fc uint8) error {
		address &= addressMask
		wait, err := c.writeWordAt(address, value, fc, offset)
		if err != nil {
			return err
		}
		appendWait(wait)
		transaction := writeTransaction(address, fc, value)
		transactions = append(transactions, transaction)
		timeline = append(timeline, BusPhase{Offset: offset, Cycles: 4, Transaction: &transaction})
		offset += 4
		return nil
	}
	readWord := func(address uint32, fc uint8) (uint16, error) {
		address &= addressMask
		value, wait, err := c.readWordAt(address, fc, offset)
		if err != nil {
			return 0, err
		}
		appendWait(wait)
		transaction := readTransaction(address, fc, value)
		transactions = append(transactions, transaction)
		timeline = append(timeline, BusPhase{Offset: offset, Cycles: 4, Transaction: &transaction})
		offset += 4
		return value, nil
	}

	for _, write := range []struct {
		address uint32
		value   uint16
	}{
		{newSP + 4, uint16(savedPC)},
		{newSP, originalSR},
		{newSP + 2, uint16(savedPC >> 16)},
	} {
		if err := writeWord(write.address, write.value, 5); err != nil {
			return StepResult{}, err
		}
	}
	vectorAddress := uint32(vector) * 4
	high, err := readWord(vectorAddress, 5)
	if err != nil {
		return StepResult{}, err
	}
	low, err := readWord(vectorAddress+2, 5)
	if err != nil {
		return StepResult{}, err
	}
	handler := uint32(high)<<16 | uint32(low)
	first, err := readWord(handler, 6)
	if err != nil {
		return StepResult{}, err
	}
	second, err := readWord(handler+2, 6)
	if err != nil {
		return StepResult{}, err
	}
	c.State.SSP = newSP
	c.State.SR = originalSR | supervisor
	c.State.SR &^= 0x8000
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = handler + 4
	return StepResult{Clocks: offset, Transactions: transactions, Timeline: timeline}, nil
}

func divideUnsignedClocks(dividend uint32, divisor uint16) uint32 {
	clocks := uint32(38)
	if dividend>>16 >= uint32(divisor) {
		return 10
	}
	work := dividend
	for iteration := 0; iteration < 15; iteration++ {
		previous := work
		work <<= 1
		if int32(previous) < 0 {
			work -= uint32(divisor) << 16
		} else {
			clocks += 2
			if work>>16 >= uint32(divisor) {
				work -= uint32(divisor) << 16
				clocks--
			}
		}
	}
	return clocks * 2
}

func divideSignedClocks(dividend uint32, divisor uint16) uint32 {
	signedDividend := int64(int32(dividend))
	signedDivisor := int64(int16(divisor))
	clocks := uint32(6)
	if signedDividend < 0 {
		clocks++
		signedDividend = -signedDividend
	}
	if signedDivisor < 0 {
		signedDivisor = -signedDivisor
	}
	if uint64(signedDividend) >= uint64(signedDivisor)<<16 {
		return (clocks + 2) * 2
	}
	quotient := uint16(uint64(signedDividend) / uint64(signedDivisor))
	clocks += 55
	if int16(divisor) >= 0 {
		if int32(dividend) >= 0 {
			clocks--
		} else {
			clocks++
		}
	}
	work := quotient
	for iteration := 0; iteration < 15; iteration++ {
		if work&0x8000 == 0 {
			clocks++
		}
		work <<= 1
	}
	return clocks * 2
}

func (c *CPU) stepASRRegister(opcode uint16) (StepResult, error) {
	return c.stepRightRegister(opcode, true, false)
}

func (c *CPU) stepLSRRegister(opcode uint16) (StepResult, error) {
	return c.stepRightRegister(opcode, false, false)
}

func (c *CPU) stepRORRegister(opcode uint16) (StepResult, error) {
	return c.stepRightRegister(opcode, false, true)
}

func (c *CPU) stepRightRegister(opcode uint16, arithmetic, rotate bool) (StepResult, error) {
	size := uint8(opcode >> 6 & 3)
	reg := uint8(opcode & 7)
	count := uint32(opcode >> 9 & 7)
	if opcode&0x0020 != 0 {
		count = c.State.D[opcode>>9&7] & 63
	} else if count == 0 {
		count = 8
	}

	bits := uint8(8 << size)
	value := c.State.D[reg]
	result := c.rightOperation(value, bits, count, arithmetic, rotate)
	switch size {
	case 0:
		c.State.D[reg] = value&0xffff_ff00 | result
	case 1:
		c.State.D[reg] = value&0xffff_0000 | result
	case 2:
		c.State.D[reg] = result
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid right-shift register size %d", size)
	}
	clocks := uint32(6) + 2*count
	if size == 2 {
		clocks += 2
	}
	return c.refillSequential(controlEA{returnPC: c.State.PC}, clocks)
}

func (c *CPU) stepASRMemory(opcode uint16) (StepResult, error) {
	return c.stepRightMemory(opcode, true, false)
}

func (c *CPU) stepLSRMemory(opcode uint16) (StepResult, error) {
	return c.stepRightMemory(opcode, false, false)
}

func (c *CPU) stepRORMemory(opcode uint16) (StepResult, error) {
	return c.stepRightMemory(opcode, false, true)
}

func (c *CPU) stepRightMemory(opcode uint16, arithmetic, rotate bool) (StepResult, error) {
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode < 2 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid right-shift memory mode %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	initialPC := c.State.PC
	address, cost, err := stream.andMemoryAddress(mode, reg, 2)
	if err != nil {
		return StepResult{}, err
	}
	if address&1 != 0 {
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: initialPC}
		return c.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), stream.transactions, 54+cost, stream.dataFC, "re", true)
	}
	value, err := c.Bus.ReadWord(address&addressMask, stream.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, readTransaction(address&addressMask, stream.dataFC, value))
	result := uint16(c.rightOperation(uint32(value), 16, 1, arithmetic, rotate))
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	if err := c.Bus.WriteWord(address&addressMask, result, stream.dataFC); err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, writeTransaction(address&addressMask, stream.dataFC, result))
	return StepResult{Clocks: 8 + cost, Transactions: stream.transactions}, nil
}

func (c *CPU) rightOperation(value uint32, bits uint8, count uint32, arithmetic, rotate bool) uint32 {
	var mask, sign uint32
	switch bits {
	case 8:
		mask, sign = 0xff, 0x80
	case 16:
		mask, sign = 0xffff, 0x8000
	case 32:
		mask, sign = 0xffff_ffff, 0x8000_0000
	default:
		panic("m68k: invalid right-shift width")
	}
	result := value & mask
	carry := false
	c.State.SR &^= 0x000f
	for shifted := uint32(0); shifted < count; shifted++ {
		carry = result&1 != 0
		fill := uint32(0)
		if rotate && carry || arithmetic && result&sign != 0 {
			fill = sign
		}
		result = result>>1 | fill
	}
	if result == 0 {
		c.State.SR |= 0x0004
	}
	if result&sign != 0 {
		c.State.SR |= 0x0008
	}
	if count != 0 && !rotate {
		c.State.SR &^= 0x0010
	}
	if carry {
		c.State.SR |= 0x0001
		if !rotate {
			c.State.SR |= 0x0010
		}
	}
	return result
}

func (c *CPU) stepASLRegister(opcode uint16) (StepResult, error) {
	return c.stepLeftRegister(opcode, true, false)
}

func (c *CPU) stepLSLRegister(opcode uint16) (StepResult, error) {
	return c.stepLeftRegister(opcode, false, false)
}

func (c *CPU) stepROLRegister(opcode uint16) (StepResult, error) {
	return c.stepLeftRegister(opcode, false, true)
}

func (c *CPU) stepLeftRegister(opcode uint16, arithmetic, rotate bool) (StepResult, error) {
	size := uint8(opcode >> 6 & 3)
	reg := uint8(opcode & 7)
	count := uint32(opcode >> 9 & 7)
	if opcode&0x0020 != 0 {
		count = c.State.D[opcode>>9&7] & 63
	} else if count == 0 {
		count = 8
	}

	bits := uint8(8 << size)
	value := c.State.D[reg]
	result := c.leftOperation(value, bits, count, arithmetic, rotate)
	switch size {
	case 0:
		c.State.D[reg] = value&0xffff_ff00 | result
	case 1:
		c.State.D[reg] = value&0xffff_0000 | result
	case 2:
		c.State.D[reg] = result
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid left-shift register size %d", size)
	}
	clocks := uint32(6) + 2*count
	if size == 2 {
		clocks += 2
	}
	return c.refillSequential(controlEA{returnPC: c.State.PC}, clocks)
}

func (c *CPU) stepASLMemory(opcode uint16) (StepResult, error) {
	return c.stepLeftMemory(opcode, true, false)
}

func (c *CPU) stepLSLMemory(opcode uint16) (StepResult, error) {
	return c.stepLeftMemory(opcode, false, false)
}

func (c *CPU) stepROLMemory(opcode uint16) (StepResult, error) {
	return c.stepLeftMemory(opcode, false, true)
}

func (c *CPU) stepLeftMemory(opcode uint16, arithmetic, rotate bool) (StepResult, error) {
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode < 2 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid left-shift memory mode %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	initialPC := c.State.PC
	address, cost, err := stream.andMemoryAddress(mode, reg, 2)
	if err != nil {
		return StepResult{}, err
	}
	if address&1 != 0 {
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: initialPC}
		return c.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), stream.transactions, 54+cost, stream.dataFC, "re", true)
	}
	value, err := c.Bus.ReadWord(address&addressMask, stream.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, readTransaction(address&addressMask, stream.dataFC, value))
	result := uint16(c.leftOperation(uint32(value), 16, 1, arithmetic, rotate))
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	if err := c.Bus.WriteWord(address&addressMask, result, stream.dataFC); err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, writeTransaction(address&addressMask, stream.dataFC, result))
	return StepResult{Clocks: 8 + cost, Transactions: stream.transactions}, nil
}

func (c *CPU) leftOperation(value uint32, bits uint8, count uint32, arithmetic, rotate bool) uint32 {
	var mask, sign uint32
	switch bits {
	case 8:
		mask, sign = 0xff, 0x80
	case 16:
		mask, sign = 0xffff, 0x8000
	case 32:
		mask, sign = 0xffff_ffff, 0x8000_0000
	default:
		panic("m68k: invalid left-shift width")
	}
	result := value & mask
	overflow := false
	carry := false
	c.State.SR &^= 0x000f
	for shifted := uint32(0); shifted < count; shifted++ {
		oldSign := result & sign
		carry = oldSign != 0
		result = result << 1 & mask
		if rotate && carry {
			result |= 1
		}
		if arithmetic && result&sign != oldSign {
			overflow = true
		}
	}
	if result == 0 {
		c.State.SR |= 0x0004
	}
	if result&sign != 0 {
		c.State.SR |= 0x0008
	}
	if overflow {
		c.State.SR |= 0x0002
	}
	if count != 0 {
		if !rotate {
			c.State.SR &^= 0x0010
		}
		if carry {
			c.State.SR |= 0x0001
			if !rotate {
				c.State.SR |= 0x0010
			}
		}
	}
	return result
}

func (c *CPU) stepTST(opcode uint16) (StepResult, error) {
	size, mode, reg := uint8(opcode>>6&3), uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid TST effective address %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	switch size {
	case 0:
		initialPC := c.State.PC
		value, cost, _, err := stream.readSource(mode, reg)
		if err != nil {
			if mode == 2 {
				var fault BusFault
				target := c.addressRegister(reg)
				if errors.As(err, &fault) {
					faultAddress, faultFC, write, faultSize := fault.M68KBusFault()
					if !write && faultSize == 1 && faultAddress == target&addressMask && target&1 == 0 {
						return c.enterByteBusError(opcode, target, initialPC-2,
							stream.transactions, 60+cost, faultFC, "re", true)
					}
				}
			}
			return StepResult{}, err
		}
		c.setLogicalFlags(uint32(value), 8)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + cost, Transactions: stream.transactions}, nil
	case 1:
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		value, cost, _, fault, err := step.readWordSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		c.setLogicalFlags(uint32(value), 16)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + cost, Transactions: step.transactions}, nil
	case 2:
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		value, cost, _, fault, err := step.readLongSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		c.setLogicalFlags(value, 32)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + cost, Transactions: step.transactions}, nil
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid TST size %d", size)
	}
}

func (c *CPU) stepLINK(opcode uint16) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	displacement, err := stream.consumeExtension()
	if err != nil {
		return StepResult{}, err
	}
	reg := uint8(opcode & 7)
	oldAddress := c.addressRegister(reg)
	stackTransactions, err := c.pushLong(oldAddress)
	if err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, stackTransactions...)
	frame := c.addressRegister(7)
	c.setAddressRegister(reg, frame)
	c.setAddressRegister(7, frame+uint32(int32(int16(displacement))))
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 16, Transactions: stream.transactions}, nil
}

func (c *CPU) stepUNLK(opcode uint16) (StepResult, error) {
	reg := uint8(opcode & 7)
	frame := c.addressRegister(reg)
	dataFC := uint8(1)
	if c.State.SR&supervisor != 0 {
		dataFC = 5
	}
	if frame&1 != 0 {
		return c.enterAddressError(opcode, frame, c.State.PC, nil, 58, dataFC, "re", true)
	}
	c.setAddressRegister(7, frame)
	high, err := c.Bus.ReadWord(frame&addressMask, dataFC)
	if err != nil {
		return StepResult{}, err
	}
	low, err := c.Bus.ReadWord((frame+2)&addressMask, dataFC)
	if err != nil {
		return StepResult{}, err
	}
	value := uint32(high)<<16 | uint32(low)
	if reg == 7 {
		c.setAddressRegister(7, value)
	} else {
		c.setAddressRegister(reg, value)
		c.setAddressRegister(7, frame+4)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: dataFC,
		transactions: []Transaction{
			readTransaction(frame&addressMask, dataFC, high),
			readTransaction((frame+2)&addressMask, dataFC, low),
		}}
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 12, Transactions: stream.transactions}, nil
}

func (c *CPU) stepMOVEM(opcode uint16) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	mask, err := stream.consumeExtension()
	if err != nil {
		return StepResult{}, err
	}
	direction := opcode>>10&1 != 0
	long := opcode&0x0040 != 0
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if direction {
		if mode == 4 || mode < 2 || mode == 7 && reg > 3 {
			return StepResult{}, fmt.Errorf("m68k: invalid MOVEM memory-to-register mode %d:%d", mode, reg)
		}
	} else if mode == 3 || mode < 2 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid MOVEM register-to-memory mode %d:%d", mode, reg)
	}
	address, eaClocks, err := stream.movemAddress(mode, reg, direction)
	if err != nil {
		return StepResult{}, err
	}
	operandFC := stream.dataFC
	if direction && mode == 7 && (reg == 2 || reg == 3) {
		operandFC = stream.programFC
	}
	faultAddress := address
	if !direction && mode == 4 {
		faultAddress -= 2
	}
	if faultAddress&1 != 0 {
		kind := "we"
		if direction {
			kind = "re"
		}
		return c.enterAddressError(opcode, faultAddress, c.State.PC, stream.transactions, 62+eaClocks, operandFC, kind, direction)
	}
	count := uint32(0)
	for bits := mask; bits != 0; bits &= bits - 1 {
		count++
	}
	if direction {
		endAddress, err := stream.movemLoad(address, mask, long, operandFC)
		if err != nil {
			return StepResult{}, err
		}
		dummy, err := c.Bus.ReadWord(endAddress&addressMask, operandFC)
		if err != nil {
			return StepResult{}, err
		}
		stream.transactions = append(stream.transactions, readTransaction(endAddress&addressMask, operandFC, dummy))
		if mode == 3 {
			step := uint32(2)
			if long {
				step = 4
			}
			c.setAddressRegister(reg, address+step*count)
		}
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		perRegister := uint32(4)
		if long {
			perRegister = 8
		}
		return StepResult{Clocks: 12 + eaClocks + perRegister*count, Transactions: stream.transactions}, nil
	}
	if err := stream.movemStore(address, mask, long, mode == 4, reg); err != nil {
		return StepResult{}, err
	}
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	perRegister := uint32(4)
	if long {
		perRegister = 8
	}
	return StepResult{Clocks: 8 + eaClocks + perRegister*count, Transactions: stream.transactions}, nil
}

func (s *moveByteStep) movemAddress(mode, reg uint8, memoryToRegisters bool) (uint32, uint32, error) {
	c := s.cpu
	switch mode {
	case 2, 3, 4:
		return c.addressRegister(reg), 0, nil
	case 5:
		extension, err := s.consumeExtension()
		return c.addressRegister(reg) + uint32(int32(int16(extension))), 4, err
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, err
		}
		index, err := c.briefIndex(extension)
		return c.addressRegister(reg) + index + uint32(int32(int8(extension))), 6, err
	case 7:
		switch reg {
		case 0:
			extension, err := s.consumeExtension()
			return uint32(int32(int16(extension))), 4, err
		case 1:
			high, err := s.consumeExtension()
			if err != nil {
				return 0, 0, err
			}
			low, err := s.consumeExtension()
			return uint32(high)<<16 | uint32(low), 8, err
		case 2:
			if !memoryToRegisters {
				break
			}
			base := c.State.PC - 2
			extension, err := s.consumeExtension()
			return base + uint32(int32(int16(extension))), 4, err
		case 3:
			if !memoryToRegisters {
				break
			}
			base := c.State.PC - 2
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, err
			}
			index, err := c.briefIndex(extension)
			return base + index + uint32(int32(int8(extension))), 6, err
		}
	}
	return 0, 0, fmt.Errorf("m68k: invalid MOVEM effective address %d:%d", mode, reg)
}

func (s *moveByteStep) movemLoad(address uint32, mask uint16, long bool, operandFC uint8) (uint32, error) {
	for register := uint8(0); register < 16; register++ {
		if mask&(uint16(1)<<register) == 0 {
			continue
		}
		high, err := s.cpu.Bus.ReadWord(address&addressMask, operandFC)
		if err != nil {
			return 0, err
		}
		s.transactions = append(s.transactions, readTransaction(address&addressMask, operandFC, high))
		address += 2
		value := uint32(int32(int16(high)))
		if long {
			low, err := s.cpu.Bus.ReadWord(address&addressMask, operandFC)
			if err != nil {
				return 0, err
			}
			s.transactions = append(s.transactions, readTransaction(address&addressMask, operandFC, low))
			address += 2
			value = uint32(high)<<16 | uint32(low)
		}
		s.cpu.setMOVEMRegister(register, value)
	}
	return address, nil
}

func (s *moveByteStep) movemStore(address uint32, mask uint16, long, predecrement bool, eaRegister uint8) error {
	var registers [16]uint32
	for register := uint8(0); register < 16; register++ {
		registers[register] = s.cpu.movemRegister(register)
	}
	for bit := uint8(0); bit < 16; bit++ {
		if mask&(uint16(1)<<bit) == 0 {
			continue
		}
		register := bit
		if predecrement {
			register = 15 - bit
		}
		value := registers[register]
		if predecrement {
			if long {
				address -= 2
				if err := s.movemWriteWord(address, uint16(value)); err != nil {
					return err
				}
			}
			address -= 2
			word := uint16(value)
			if long {
				word = uint16(value >> 16)
			}
			if err := s.movemWriteWord(address, word); err != nil {
				return err
			}
			s.cpu.setAddressRegister(eaRegister, address)
			continue
		}
		if long {
			if err := s.movemWriteWord(address, uint16(value>>16)); err != nil {
				return err
			}
			address += 2
		}
		if err := s.movemWriteWord(address, uint16(value)); err != nil {
			return err
		}
		address += 2
	}
	return nil
}

func (s *moveByteStep) movemWriteWord(address uint32, value uint16) error {
	if err := s.cpu.Bus.WriteWord(address&addressMask, value, s.dataFC); err != nil {
		return err
	}
	s.transactions = append(s.transactions, writeTransaction(address&addressMask, s.dataFC, value))
	return nil
}

func (c *CPU) movemRegister(register uint8) uint32 {
	if register < 8 {
		return c.State.D[register]
	}
	return c.addressRegister(register - 8)
}

func (c *CPU) setMOVEMRegister(register uint8, value uint32) {
	if register < 8 {
		c.State.D[register] = value
		return
	}
	c.setAddressRegister(register-8, value)
}

func (c *CPU) stepCLR(opcode uint16) (StepResult, error) {
	size, mode, reg := uint8(opcode>>6&3), uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid CLR destination mode %d:%d", mode, reg)
	}
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	if mode == 0 {
		switch size {
		case 0:
			c.State.D[reg] &= 0xffff_ff00
		case 1:
			c.State.D[reg] &= 0xffff_0000
		case 2:
			c.State.D[reg] = 0
		}
		c.setLogicalFlags(0, 8<<size)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		clocks := uint32(4)
		if size == 2 {
			clocks = 6
		}
		return StepResult{Clocks: clocks, Transactions: stream.transactions}, nil
	}
	switch size {
	case 0:
		return stream.logicalByteMemory(mode, reg, 0, 8, logicalAND)
	case 1:
		return stream.logicalWordMemory(opcode, mode, reg, 0, 8, logicalAND)
	case 2:
		return stream.logicalLongMemory(opcode, mode, reg, 0, 12, logicalAND)
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid CLR size %d", size)
	}
}

func (c *CPU) stepADDToDataRegister(opcode uint16) (StepResult, error) {
	return c.stepArithmeticToDataRegister(opcode, false)
}

func (c *CPU) stepSUBToDataRegister(opcode uint16) (StepResult, error) {
	return c.stepArithmeticToDataRegister(opcode, true)
}

func (c *CPU) stepArithmeticToDataRegister(opcode uint16, subtract bool) (StepResult, error) {
	opmode := uint8(opcode >> 6 & 7)
	destination := uint8(opcode >> 9 & 7)
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	switch opmode {
	case 0:
		source, cost, _, err := stream.readSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		dest := byte(c.State.D[destination])
		result := arithmeticByte(dest, source, subtract)
		c.State.D[destination] = c.State.D[destination]&0xffff_ff00 | uint32(result)
		c.setArithmeticFlags(uint32(dest), uint32(source), uint32(result), 8, subtract)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + cost, Transactions: stream.transactions}, nil
	case 1:
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		source, cost, _, fault, err := step.readWordSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		dest := uint16(c.State.D[destination])
		result := arithmeticWord(dest, source, subtract)
		c.State.D[destination] = c.State.D[destination]&0xffff_0000 | uint32(result)
		c.setArithmeticFlags(uint32(dest), uint32(source), uint32(result), 16, subtract)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + cost, Transactions: step.transactions}, nil
	case 2:
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		source, cost, memory, fault, err := step.readLongSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		dest := c.State.D[destination]
		result := arithmeticLong(dest, source, subtract)
		c.State.D[destination] = result
		c.setArithmeticFlags(dest, source, result, 32, subtract)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		clocks := uint32(6) + cost
		if !memory {
			clocks += 2
		}
		return StepResult{Clocks: clocks, Transactions: step.transactions}, nil
	}
	return StepResult{}, fmt.Errorf("m68k: invalid ADD opmode %d", opmode)
}

func (c *CPU) stepADDToMemory(opcode uint16) (StepResult, error) {
	return c.stepArithmeticToMemory(opcode, false)
}

func (c *CPU) stepSUBToMemory(opcode uint16) (StepResult, error) {
	return c.stepArithmeticToMemory(opcode, true)
}

func (c *CPU) stepArithmeticToMemory(opcode uint16, subtract bool) (StepResult, error) {
	size := uint8(opcode >> 6 & 3)
	base := uint32(8)
	if size == 2 {
		base = 12
	}
	return c.stepArithmeticMemory(opcode, size, uint8(opcode>>3&7), uint8(opcode&7), c.State.D[opcode>>9&7], base, subtract)
}

func (c *CPU) stepADDImmediate(opcode uint16) (StepResult, error) {
	return c.stepArithmeticImmediate(opcode, false)
}

func (c *CPU) stepSUBImmediate(opcode uint16) (StepResult, error) {
	return c.stepArithmeticImmediate(opcode, true)
}

func (c *CPU) stepArithmeticImmediate(opcode uint16, subtract bool) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	mode, reg, size := uint8(opcode>>3&7), uint8(opcode&7), uint8(opcode>>6&3)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid ADDI destination mode %d:%d", mode, reg)
	}
	if size < 2 {
		immediate, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		if mode == 0 {
			if size == 0 {
				dest, source := byte(c.State.D[reg]), byte(immediate)
				result := arithmeticByte(dest, source, subtract)
				c.State.D[reg] = c.State.D[reg]&0xffff_ff00 | uint32(result)
				c.setArithmeticFlags(uint32(dest), uint32(source), uint32(result), 8, subtract)
			} else {
				dest, source := uint16(c.State.D[reg]), immediate
				result := arithmeticWord(dest, source, subtract)
				c.State.D[reg] = c.State.D[reg]&0xffff_0000 | uint32(result)
				c.setArithmeticFlags(uint32(dest), uint32(source), uint32(result), 16, subtract)
			}
			if err := stream.refill(); err != nil {
				return StepResult{}, err
			}
			return StepResult{Clocks: 8, Transactions: stream.transactions}, nil
		}
		return c.stepArithmeticMemoryWithStream(opcode, size, mode, reg, uint32(immediate), 12, stream, subtract)
	}
	high, err := stream.consumeExtension()
	if err != nil {
		return StepResult{}, err
	}
	low, err := stream.consumeExtension()
	if err != nil {
		return StepResult{}, err
	}
	immediate := uint32(high)<<16 | uint32(low)
	if mode == 0 {
		dest := c.State.D[reg]
		result := arithmeticLong(dest, immediate, subtract)
		c.State.D[reg] = result
		c.setArithmeticFlags(dest, immediate, result, 32, subtract)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 16, Transactions: stream.transactions}, nil
	}
	return c.stepArithmeticMemoryWithStream(opcode, 2, mode, reg, immediate, 20, stream, subtract)
}

func (c *CPU) stepADDQuick(opcode uint16) (StepResult, error) {
	return c.stepArithmeticQuick(opcode, false)
}

func (c *CPU) stepSUBQuick(opcode uint16) (StepResult, error) {
	return c.stepArithmeticQuick(opcode, true)
}

func (c *CPU) stepArithmeticQuick(opcode uint16, subtract bool) (StepResult, error) {
	size, mode, reg := uint8(opcode>>6&3), uint8(opcode>>3&7), uint8(opcode&7)
	quick := uint32(opcode >> 9 & 7)
	if quick == 0 {
		quick = 8
	}
	if mode == 1 {
		if size == 0 {
			return StepResult{}, fmt.Errorf("m68k: ADDQ.B to An is invalid")
		}
		c.setAddressRegister(reg, arithmeticLong(c.addressRegister(reg), quick, subtract))
		return c.refillSequential(controlEA{returnPC: c.State.PC}, 8)
	}
	if mode == 0 {
		switch size {
		case 0:
			dest, source := byte(c.State.D[reg]), byte(quick)
			result := arithmeticByte(dest, source, subtract)
			c.State.D[reg] = c.State.D[reg]&0xffff_ff00 | uint32(result)
			c.setArithmeticFlags(uint32(dest), uint32(source), uint32(result), 8, subtract)
		case 1:
			dest, source := uint16(c.State.D[reg]), uint16(quick)
			result := arithmeticWord(dest, source, subtract)
			c.State.D[reg] = c.State.D[reg]&0xffff_0000 | uint32(result)
			c.setArithmeticFlags(uint32(dest), uint32(source), uint32(result), 16, subtract)
		case 2:
			dest := c.State.D[reg]
			result := arithmeticLong(dest, quick, subtract)
			c.State.D[reg] = result
			c.setArithmeticFlags(dest, quick, result, 32, subtract)
		}
		clocks := uint32(4)
		if size == 2 {
			clocks = 8
		}
		return c.refillSequential(controlEA{returnPC: c.State.PC}, clocks)
	}
	base := uint32(8)
	if size == 2 {
		base = 12
	}
	return c.stepArithmeticMemory(opcode, size, mode, reg, quick, base, subtract)
}

func (c *CPU) stepADDMemory(opcode uint16, size, mode, reg uint8, operand uint32, base uint32) (StepResult, error) {
	return c.stepArithmeticMemory(opcode, size, mode, reg, operand, base, false)
}

func (c *CPU) stepArithmeticMemory(opcode uint16, size, mode, reg uint8, operand uint32, base uint32, subtract bool) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	return c.stepArithmeticMemoryWithStream(opcode, size, mode, reg, operand, base, stream, subtract)
}

func (c *CPU) stepArithmeticMemoryWithStream(opcode uint16, size, mode, reg uint8, operand uint32, base uint32, stream moveByteStep, subtract bool) (StepResult, error) {
	if mode < 2 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid ADD memory mode %d:%d", mode, reg)
	}
	if size == 0 {
		var address, delta, cost uint32
		switch mode {
		case 2:
			address, cost = c.addressRegister(reg), 4
		case 3:
			address, delta, cost = c.addressRegister(reg), byteAddressDelta(reg), 4
		case 4:
			delta = byteAddressDelta(reg)
			address, cost = c.addressRegister(reg)-delta, 6
			c.setAddressRegister(reg, address)
		case 5:
			ext, err := stream.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			address, cost = c.addressRegister(reg)+uint32(int32(int16(ext))), 8
		case 6:
			ext, err := stream.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			index, err := c.briefIndex(ext)
			if err != nil {
				return StepResult{}, err
			}
			address, cost = c.addressRegister(reg)+index+uint32(int32(int8(ext))), 10
		case 7:
			if reg == 0 {
				ext, err := stream.consumeExtension()
				if err != nil {
					return StepResult{}, err
				}
				address, cost = uint32(int32(int16(ext))), 8
			} else {
				high, err := stream.consumeExtension()
				if err != nil {
					return StepResult{}, err
				}
				low, err := stream.consumeExtension()
				if err != nil {
					return StepResult{}, err
				}
				address, cost = uint32(high)<<16|uint32(low), 12
			}
		}
		value, err := c.Bus.ReadByte(address&addressMask, stream.dataFC)
		if err != nil {
			return StepResult{}, err
		}
		stream.transactions = append(stream.transactions, readByteTransaction(address&addressMask, stream.dataFC, value))
		result := arithmeticByte(value, byte(operand), subtract)
		c.setArithmeticFlags(uint32(value), uint32(byte(operand)), uint32(result), 8, subtract)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		if err := stream.writeByte(address, result); err != nil {
			return StepResult{}, err
		}
		if mode == 3 {
			c.setAddressRegister(reg, c.addressRegister(reg)+delta)
		}
		return StepResult{Clocks: base + cost, Transactions: stream.transactions}, nil
	}
	initialPC := c.State.PC
	bytes := uint32(2)
	if size == 2 {
		bytes = 4
	}
	address, cost, err := stream.andMemoryAddress(mode, reg, bytes)
	if err != nil {
		return StepResult{}, err
	}
	if address&1 != 0 {
		if size == 1 {
			step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: initialPC}
			return c.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), stream.transactions, 54+cost+(base-8), stream.dataFC, "re", true)
		}
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: initialPC}
		return c.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), stream.transactions, 50+cost+(base-12), stream.dataFC, "re", true)
	}
	if size == 1 {
		value, err := c.Bus.ReadWord(address&addressMask, stream.dataFC)
		if err != nil {
			return StepResult{}, err
		}
		stream.transactions = append(stream.transactions, readTransaction(address&addressMask, stream.dataFC, value))
		result := arithmeticWord(value, uint16(operand), subtract)
		c.setArithmeticFlags(uint32(value), uint32(uint16(operand)), uint32(result), 16, subtract)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		if err := c.Bus.WriteWord(address&addressMask, result, stream.dataFC); err != nil {
			return StepResult{}, err
		}
		stream.transactions = append(stream.transactions, writeTransaction(address&addressMask, stream.dataFC, result))
		return StepResult{Clocks: base + cost, Transactions: stream.transactions}, nil
	}
	if mode == 3 {
		c.setAddressRegister(reg, c.addressRegister(reg)+4)
	}
	high, err := c.Bus.ReadWord(address&addressMask, stream.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	low, err := c.Bus.ReadWord((address+2)&addressMask, stream.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, readTransaction(address&addressMask, stream.dataFC, high), readTransaction((address+2)&addressMask, stream.dataFC, low))
	value := uint32(high)<<16 | uint32(low)
	result := arithmeticLong(value, operand, subtract)
	c.setArithmeticFlags(value, operand, result, 32, subtract)
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	if err := c.Bus.WriteWord((address+2)&addressMask, uint16(result), stream.dataFC); err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, writeTransaction((address+2)&addressMask, stream.dataFC, uint16(result)))
	if err := c.Bus.WriteWord(address&addressMask, uint16(result>>16), stream.dataFC); err != nil {
		return StepResult{}, err
	}
	stream.transactions = append(stream.transactions, writeTransaction(address&addressMask, stream.dataFC, uint16(result>>16)))
	return StepResult{Clocks: base + cost, Transactions: stream.transactions}, nil
}

func (c *CPU) setAdditionFlags(destination, source, result uint32, bits uint8) {
	var mask, sign uint32
	switch bits {
	case 8:
		mask, sign = 0xff, 0x80
	case 16:
		mask, sign = 0xffff, 0x8000
	case 32:
		mask, sign = 0xffff_ffff, 0x8000_0000
	default:
		panic("m68k: invalid addition width")
	}
	destination &= mask
	source &= mask
	result &= mask
	c.State.SR &^= 0x001f
	if result == 0 {
		c.State.SR |= 0x0004
	}
	if result&sign != 0 {
		c.State.SR |= 0x0008
	}
	if ^(destination^source)&(destination^result)&sign != 0 {
		c.State.SR |= 0x0002
	}
	if source > mask-destination {
		c.State.SR |= 0x0011
	}
}

func (c *CPU) setArithmeticFlags(destination, source, result uint32, bits uint8, subtract bool) {
	if !subtract {
		c.setAdditionFlags(destination, source, result, bits)
		return
	}
	c.setCompareFlags(destination, source, bits)
	var mask uint32
	switch bits {
	case 8:
		mask = 0xff
	case 16:
		mask = 0xffff
	case 32:
		mask = 0xffff_ffff
	default:
		panic("m68k: invalid subtraction width")
	}
	if source&mask > destination&mask {
		c.State.SR |= 0x0010
	} else {
		c.State.SR &^= 0x0010
	}
}

func arithmeticByte(destination, source byte, subtract bool) byte {
	if subtract {
		return destination - source
	}
	return destination + source
}

func arithmeticWord(destination, source uint16, subtract bool) uint16 {
	if subtract {
		return destination - source
	}
	return destination + source
}

func arithmeticLong(destination, source uint32, subtract bool) uint32 {
	if subtract {
		return destination - source
	}
	return destination + source
}

func (c *CPU) stepEXG(opcode uint16, form uint8) (StepResult, error) {
	left, right := uint8(opcode>>9&7), uint8(opcode&7)
	switch form {
	case 0:
		c.State.D[left], c.State.D[right] = c.State.D[right], c.State.D[left]
	case 1:
		leftValue, rightValue := c.addressRegister(left), c.addressRegister(right)
		c.setAddressRegister(left, rightValue)
		c.setAddressRegister(right, leftValue)
	case 2:
		dataValue, addressValue := c.State.D[left], c.addressRegister(right)
		c.State.D[left] = addressValue
		c.setAddressRegister(right, dataValue)
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid EXG form %d", form)
	}
	return c.refillSequential(controlEA{returnPC: c.State.PC}, 6)
}

func (c *CPU) stepCMP(opcode uint16) (StepResult, error) {
	opmode := uint8(opcode >> 6 & 7)
	destination := uint8(opcode >> 9 & 7)
	sourceMode, sourceReg := uint8(opcode>>3&7), uint8(opcode&7)
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	switch opmode {
	case 0:
		source, sourceCost, _, err := stream.readSource(sourceMode, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		c.setCompareFlags(uint32(byte(c.State.D[destination])), uint32(source), 8)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + sourceCost, Transactions: stream.transactions}, nil
	case 1:
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		source, sourceCost, _, fault, err := step.readWordSource(sourceMode, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		c.setCompareFlags(uint32(uint16(c.State.D[destination])), uint32(source), 16)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + sourceCost, Transactions: step.transactions}, nil
	case 2:
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		source, sourceCost, _, fault, err := step.readLongSource(sourceMode, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		c.setCompareFlags(c.State.D[destination], source, 32)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		clocks := uint32(6) + sourceCost
		return StepResult{Clocks: clocks, Transactions: step.transactions}, nil
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid CMP opmode %d", opmode)
	}
}

func (c *CPU) stepCMPA(opcode uint16, long bool) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	destination := c.addressRegister(uint8(opcode >> 9 & 7))
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if !long {
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		source, cost, _, fault, err := step.readWordSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		c.setCompareFlags(destination, uint32(int32(int16(source))), 32)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 6 + cost, Transactions: step.transactions}, nil
	}
	step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	source, cost, _, fault, err := step.readLongSource(mode, reg)
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	c.setCompareFlags(destination, source, 32)
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 6 + cost, Transactions: step.transactions}, nil
}

func (c *CPU) stepCMPMemory(opcode uint16) (StepResult, error) {
	sourceReg, destinationReg := uint8(opcode&7), uint8(opcode>>9&7)
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	switch opcode >> 6 & 3 {
	case 0:
		source, _, _, err := stream.readSource(3, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		destination, _, _, err := stream.readSource(3, destinationReg)
		if err != nil {
			return StepResult{}, err
		}
		c.setCompareFlags(uint32(destination), uint32(source), 8)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 12, Transactions: stream.transactions}, nil
	case 1:
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		sourceAddress := c.addressRegister(sourceReg)
		if sourceAddress&1 != 0 {
			c.setAddressRegister(sourceReg, sourceAddress+2)
			return c.enterCMPMAddressError(opcode, sourceAddress, step.initialPC-2, step.transactions, 58, step.dataFC)
		}
		source, _, _, fault, err := step.readWordSource(3, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		destinationAddress := c.addressRegister(destinationReg)
		if destinationAddress&1 != 0 {
			return c.enterCMPMAddressError(opcode, destinationAddress, step.initialPC-2, step.transactions, 62, step.dataFC)
		}
		destination, _, _, fault, err := step.readWordSource(3, destinationReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		c.setCompareFlags(uint32(destination), uint32(source), 16)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 12, Transactions: step.transactions}, nil
	case 2:
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		sourceAddress := c.addressRegister(sourceReg)
		if sourceAddress&1 != 0 {
			c.setAddressRegister(sourceReg, sourceAddress+2)
			return c.enterCMPMAddressError(opcode, sourceAddress, step.initialPC-2, step.transactions, 58, step.dataFC)
		}
		source, _, _, fault, err := step.readLongSource(3, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		destinationAddress := c.addressRegister(destinationReg)
		if destinationAddress&1 != 0 {
			return c.enterCMPMAddressError(opcode, destinationAddress, step.initialPC-2, step.transactions, 66, step.dataFC)
		}
		destination, _, _, fault, err := step.readLongSource(3, destinationReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		c.setCompareFlags(destination, source, 32)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 20, Transactions: step.transactions}, nil
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid CMPM size in opcode 0x%04x", opcode)
	}
}

func (c *CPU) enterCMPMAddressError(opcode uint16, target, savedPC uint32, prefix []Transaction, clocks uint32, faultFC uint8) (StepResult, error) {
	return c.enterAddressError(opcode, target, savedPC+2, prefix, clocks, faultFC, "re", true)
}

func (c *CPU) stepCMPImmediate(opcode uint16) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	if mode == 1 || mode == 7 && reg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid CMPI destination mode %d:%d", mode, reg)
	}
	switch opcode >> 6 & 3 {
	case 0:
		immediate, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		destination, destinationCost, _, err := stream.readSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		c.setCompareFlags(uint32(destination), uint32(byte(immediate)), 8)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 8 + destinationCost, Transactions: stream.transactions}, nil
	case 1:
		immediate, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		destination, destinationCost, _, fault, err := step.readWordSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			fault.Clocks += 4
			return *fault, nil
		}
		c.setCompareFlags(uint32(destination), uint32(immediate), 16)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 8 + destinationCost, Transactions: step.transactions}, nil
	case 2:
		high, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		low, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		immediate := uint32(high)<<16 | uint32(low)
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		destination, destinationCost, destinationMemory, fault, err := step.readLongSource(mode, reg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			fault.Clocks += 8
			return *fault, nil
		}
		c.setCompareFlags(destination, immediate, 32)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		clocks := uint32(14) + destinationCost
		if destinationMemory {
			clocks -= 2
		}
		return StepResult{Clocks: clocks, Transactions: step.transactions}, nil
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid CMPI size in opcode 0x%04x", opcode)
	}
}

func (c *CPU) setCompareFlags(destination, source uint32, bits uint8) {
	var mask, sign uint32
	switch bits {
	case 8:
		mask, sign = 0xff, 0x80
	case 16:
		mask, sign = 0xffff, 0x8000
	case 32:
		mask, sign = 0xffff_ffff, 0x8000_0000
	default:
		panic("m68k: invalid compare width")
	}
	destination &= mask
	source &= mask
	result := (destination - source) & mask
	c.State.SR &^= 0x000f
	if result == 0 {
		c.State.SR |= 0x0004
	}
	if result&sign != 0 {
		c.State.SR |= 0x0008
	}
	if (destination^source)&(destination^result)&sign != 0 {
		c.State.SR |= 0x0002
	}
	if source > destination {
		c.State.SR |= 0x0001
	}
}

func (c *CPU) stepANDImmediate(opcode uint16) (StepResult, error) {
	return c.stepLogicalImmediate(opcode, logicalAND)
}

func (c *CPU) stepORImmediate(opcode uint16) (StepResult, error) {
	return c.stepLogicalImmediate(opcode, logicalOR)
}

func (c *CPU) stepEORImmediate(opcode uint16) (StepResult, error) {
	return c.stepLogicalImmediate(opcode, logicalEOR)
}

func (c *CPU) stepLogicalImmediate(opcode uint16, operation logicalOperation) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	mode, reg := uint8(opcode>>3&7), uint8(opcode&7)
	switch opcode >> 6 & 3 {
	case 0:
		immediate, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		if mode == 0 {
			result := logicalByte(byte(c.State.D[reg]), byte(immediate), operation)
			c.State.D[reg] = c.State.D[reg]&0xffff_ff00 | uint32(result)
			c.setLogicalFlags(uint32(result), 8)
			if err := stream.refill(); err != nil {
				return StepResult{}, err
			}
			return StepResult{Clocks: 8, Transactions: stream.transactions}, nil
		}
		return stream.logicalByteMemory(mode, reg, byte(immediate), 12, operation)
	case 1:
		immediate, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		if mode == 0 {
			result := logicalWord(uint16(c.State.D[reg]), immediate, operation)
			c.State.D[reg] = c.State.D[reg]&0xffff_0000 | uint32(result)
			c.setLogicalFlags(uint32(result), 16)
			if err := stream.refill(); err != nil {
				return StepResult{}, err
			}
			return StepResult{Clocks: 8, Transactions: stream.transactions}, nil
		}
		return stream.logicalWordMemory(opcode, mode, reg, immediate, 12, operation)
	case 2:
		high, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		low, err := stream.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		immediate := uint32(high)<<16 | uint32(low)
		if mode == 0 {
			result := logicalLong(c.State.D[reg], immediate, operation)
			c.State.D[reg] = result
			c.setLogicalFlags(result, 32)
			if err := stream.refill(); err != nil {
				return StepResult{}, err
			}
			return StepResult{Clocks: 16, Transactions: stream.transactions}, nil
		}
		return stream.logicalLongMemory(opcode, mode, reg, immediate, 20, operation)
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid ANDI size in opcode 0x%04x", opcode)
	}
}

func (c *CPU) stepANDByteToMemory(opcode uint16) (StepResult, error) {
	return c.stepLogicalByteToMemory(opcode, logicalAND)
}

func (c *CPU) stepORByteToMemory(opcode uint16) (StepResult, error) {
	return c.stepLogicalByteToMemory(opcode, logicalOR)
}

func (c *CPU) stepEORByteToDestination(opcode uint16) (StepResult, error) {
	if opcode>>3&7 == 0 {
		return c.stepEORToDataRegister(opcode, 8)
	}
	return c.stepLogicalByteToMemory(opcode, logicalEOR)
}

func (c *CPU) stepLogicalByteToMemory(opcode uint16, operation logicalOperation) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	return stream.logicalByteMemory(uint8(opcode>>3&7), uint8(opcode&7), byte(c.State.D[opcode>>9&7]), 8, operation)
}

func (s *moveByteStep) logicalByteMemory(mode, reg uint8, operand byte, baseClocks uint32, operation logicalOperation) (StepResult, error) {
	c := s.cpu
	var address, delta uint32
	var eaCost uint32
	switch mode {
	case 2:
		address, eaCost = c.addressRegister(reg), 4
	case 3:
		address, delta, eaCost = c.addressRegister(reg), byteAddressDelta(reg), 4
	case 4:
		delta = byteAddressDelta(reg)
		address, eaCost = c.addressRegister(reg)-delta, 6
		c.setAddressRegister(reg, address)
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		address, eaCost = c.addressRegister(reg)+uint32(int32(int16(extension))), 8
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return StepResult{}, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return StepResult{}, err
		}
		address = c.addressRegister(reg) + index + uint32(int32(int8(extension)))
		eaCost = 10
	case 7:
		switch reg {
		case 0:
			extension, err := s.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			address, eaCost = uint32(int32(int16(extension))), 8
		case 1:
			high, err := s.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			low, err := s.consumeExtension()
			if err != nil {
				return StepResult{}, err
			}
			address, eaCost = uint32(high)<<16|uint32(low), 12
		default:
			return StepResult{}, fmt.Errorf("m68k: invalid AND.B memory mode %d:%d", mode, reg)
		}
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid AND.B memory mode %d:%d", mode, reg)
	}
	value, err := c.Bus.ReadByte(address&addressMask, s.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	s.transactions = append(s.transactions, readByteTransaction(address&addressMask, s.dataFC, value))
	result := logicalByte(value, operand, operation)
	c.setLogicalFlags(uint32(result), 8)
	if err := s.refill(); err != nil {
		return StepResult{}, err
	}
	if err := s.writeByte(address, result); err != nil {
		return StepResult{}, err
	}
	if mode == 3 {
		c.setAddressRegister(reg, c.addressRegister(reg)+delta)
	}
	return StepResult{Clocks: baseClocks + eaCost, Transactions: s.transactions}, nil
}

func (c *CPU) stepANDWordToMemory(opcode uint16) (StepResult, error) {
	return c.stepLogicalWordToMemory(opcode, logicalAND)
}

func (c *CPU) stepORWordToMemory(opcode uint16) (StepResult, error) {
	return c.stepLogicalWordToMemory(opcode, logicalOR)
}

func (c *CPU) stepEORWordToDestination(opcode uint16) (StepResult, error) {
	if opcode>>3&7 == 0 {
		return c.stepEORToDataRegister(opcode, 16)
	}
	return c.stepLogicalWordToMemory(opcode, logicalEOR)
}

func (c *CPU) stepLogicalWordToMemory(opcode uint16, operation logicalOperation) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	return stream.logicalWordMemory(opcode, uint8(opcode>>3&7), uint8(opcode&7), uint16(c.State.D[opcode>>9&7]), 8, operation)
}

func (s *moveByteStep) logicalWordMemory(opcode uint16, mode, reg uint8, operand uint16, baseClocks uint32, operation logicalOperation) (StepResult, error) {
	initialPC := s.cpu.State.PC
	address, eaCost, err := s.andMemoryAddress(mode, reg, 2)
	if err != nil {
		return StepResult{}, err
	}
	if address&1 != 0 {
		step := moveWordStep{moveByteStep: *s, opcode: opcode, initialPC: initialPC}
		return s.cpu.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), s.transactions, 54+eaCost+(baseClocks-8), s.dataFC, "re", true)
	}
	value, err := s.cpu.Bus.ReadWord(address&addressMask, s.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	s.transactions = append(s.transactions, readTransaction(address&addressMask, s.dataFC, value))
	result := logicalWord(value, operand, operation)
	if operation != logicalReplace {
		s.cpu.setLogicalFlags(uint32(result), 16)
	}
	if err := s.refill(); err != nil {
		return StepResult{}, err
	}
	if err := s.cpu.Bus.WriteWord(address&addressMask, result, s.dataFC); err != nil {
		return StepResult{}, err
	}
	s.transactions = append(s.transactions, writeTransaction(address&addressMask, s.dataFC, result))
	return StepResult{Clocks: baseClocks + eaCost, Transactions: s.transactions}, nil
}

func (c *CPU) stepANDLongToMemory(opcode uint16) (StepResult, error) {
	return c.stepLogicalLongToMemory(opcode, logicalAND)
}

func (c *CPU) stepORLongToMemory(opcode uint16) (StepResult, error) {
	return c.stepLogicalLongToMemory(opcode, logicalOR)
}

func (c *CPU) stepEORLongToDestination(opcode uint16) (StepResult, error) {
	if opcode>>3&7 == 0 {
		return c.stepEORToDataRegister(opcode, 32)
	}
	return c.stepLogicalLongToMemory(opcode, logicalEOR)
}

func (c *CPU) stepLogicalLongToMemory(opcode uint16, operation logicalOperation) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	return stream.logicalLongMemory(opcode, uint8(opcode>>3&7), uint8(opcode&7), c.State.D[opcode>>9&7], 12, operation)
}

func (s *moveByteStep) logicalLongMemory(opcode uint16, mode, reg uint8, operand uint32, baseClocks uint32, operation logicalOperation) (StepResult, error) {
	initialPC := s.cpu.State.PC
	address, eaCost, err := s.andMemoryAddress(mode, reg, 4)
	if err != nil {
		return StepResult{}, err
	}
	if address&1 != 0 {
		step := moveLongStep{moveByteStep: *s, opcode: opcode, initialPC: initialPC}
		return s.cpu.enterAddressError(opcode, address, step.sourceFaultPC(mode, reg), s.transactions, 50+eaCost+(baseClocks-12), s.dataFC, "re", true)
	}
	if mode == 3 {
		s.cpu.setAddressRegister(reg, s.cpu.addressRegister(reg)+4)
	}
	high, err := s.cpu.Bus.ReadWord(address&addressMask, s.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	s.transactions = append(s.transactions, readTransaction(address&addressMask, s.dataFC, high))
	low, err := s.cpu.Bus.ReadWord((address+2)&addressMask, s.dataFC)
	if err != nil {
		return StepResult{}, err
	}
	s.transactions = append(s.transactions, readTransaction((address+2)&addressMask, s.dataFC, low))
	result := logicalLong(uint32(high)<<16|uint32(low), operand, operation)
	s.cpu.setLogicalFlags(result, 32)
	if err := s.refill(); err != nil {
		return StepResult{}, err
	}
	if err := s.cpu.Bus.WriteWord((address+2)&addressMask, uint16(result), s.dataFC); err != nil {
		return StepResult{}, err
	}
	s.transactions = append(s.transactions, writeTransaction((address+2)&addressMask, s.dataFC, uint16(result)))
	if err := s.cpu.Bus.WriteWord(address&addressMask, uint16(result>>16), s.dataFC); err != nil {
		return StepResult{}, err
	}
	s.transactions = append(s.transactions, writeTransaction(address&addressMask, s.dataFC, uint16(result>>16)))
	return StepResult{Clocks: baseClocks + eaCost, Transactions: s.transactions}, nil
}

func (s *moveByteStep) andMemoryAddress(mode, reg uint8, size uint32) (uint32, uint32, error) {
	c := s.cpu
	switch mode {
	case 2:
		return c.addressRegister(reg), 4 * size / 2, nil
	case 3:
		address := c.addressRegister(reg)
		if size == 2 {
			c.setAddressRegister(reg, address+size)
		}
		return address, 4 * size / 2, nil
	case 4:
		address := c.addressRegister(reg) - size
		c.setAddressRegister(reg, address)
		return address, 4*size/2 + 2, nil
	case 5:
		extension, err := s.consumeExtension()
		return c.addressRegister(reg) + uint32(int32(int16(extension))), 4*size/2 + 4, err
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, err
		}
		index, err := c.briefIndex(extension)
		return c.addressRegister(reg) + index + uint32(int32(int8(extension))), 4*size/2 + 6, err
	case 7:
		switch reg {
		case 0:
			extension, err := s.consumeExtension()
			return uint32(int32(int16(extension))), 4*size/2 + 4, err
		case 1:
			high, err := s.consumeExtension()
			if err != nil {
				return 0, 0, err
			}
			low, err := s.consumeExtension()
			return uint32(high)<<16 | uint32(low), 4*size/2 + 8, err
		}
	}
	return 0, 0, fmt.Errorf("m68k: invalid AND memory mode %d:%d", mode, reg)
}

func (c *CPU) stepANDToDataRegister(opcode uint16) (StepResult, error) {
	return c.stepLogicalToDataRegister(opcode, logicalAND)
}

func (c *CPU) stepORToDataRegister(opcode uint16) (StepResult, error) {
	return c.stepLogicalToDataRegister(opcode, logicalOR)
}

func (c *CPU) stepLogicalToDataRegister(opcode uint16, operation logicalOperation) (StepResult, error) {
	opmode := uint8(opcode >> 6 & 7)
	destination := uint8(opcode >> 9 & 7)
	sourceMode := uint8(opcode >> 3 & 7)
	sourceReg := uint8(opcode & 7)
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}

	switch opmode {
	case 0:
		value, sourceCost, _, err := stream.readSource(sourceMode, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		result := logicalByte(byte(c.State.D[destination]), value, operation)
		c.State.D[destination] = c.State.D[destination]&0xffff_ff00 | uint32(result)
		c.setLogicalFlags(uint32(result), 8)
		if err := stream.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + sourceCost, Transactions: stream.transactions}, nil
	case 1:
		step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		value, sourceCost, _, fault, err := step.readWordSource(sourceMode, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		result := logicalWord(uint16(c.State.D[destination]), value, operation)
		c.State.D[destination] = c.State.D[destination]&0xffff_0000 | uint32(result)
		c.setLogicalFlags(uint32(result), 16)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		return StepResult{Clocks: 4 + sourceCost, Transactions: step.transactions}, nil
	case 2:
		step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
		value, sourceCost, sourceMemory, fault, err := step.readLongSource(sourceMode, sourceReg)
		if err != nil {
			return StepResult{}, err
		}
		if fault != nil {
			return *fault, nil
		}
		result := logicalLong(c.State.D[destination], value, operation)
		c.State.D[destination] = result
		c.setLogicalFlags(result, 32)
		if err := step.refill(); err != nil {
			return StepResult{}, err
		}
		clocks := uint32(6) + sourceCost
		if !sourceMemory {
			clocks += 2
		}
		return StepResult{Clocks: clocks, Transactions: step.transactions}, nil
	default:
		return StepResult{}, fmt.Errorf("m68k: invalid AND opmode %d", opmode)
	}
}

type logicalOperation uint8

const (
	logicalAND logicalOperation = iota
	logicalOR
	logicalEOR
	logicalReplace
)

func (c *CPU) stepEORToDataRegister(opcode uint16, bits uint8) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	source := c.State.D[opcode>>9&7]
	destination := uint8(opcode & 7)
	result := logicalLong(c.State.D[destination], source, logicalEOR)
	switch bits {
	case 8:
		c.State.D[destination] = c.State.D[destination]&0xffff_ff00 | result&0xff
	case 16:
		c.State.D[destination] = c.State.D[destination]&0xffff_0000 | result&0xffff
	case 32:
		c.State.D[destination] = result
	}
	c.setLogicalFlags(result, bits)
	if err := stream.refill(); err != nil {
		return StepResult{}, err
	}
	clocks := uint32(4)
	if bits == 32 {
		clocks = 8
	}
	return StepResult{Clocks: clocks, Transactions: stream.transactions}, nil
}

func logicalByte(left, right byte, operation logicalOperation) byte {
	return byte(logicalLong(uint32(left), uint32(right), operation))
}

func logicalWord(left, right uint16, operation logicalOperation) uint16 {
	return uint16(logicalLong(uint32(left), uint32(right), operation))
}

func logicalLong(left, right uint32, operation logicalOperation) uint32 {
	switch operation {
	case logicalOR:
		return left | right
	case logicalEOR:
		return left ^ right
	case logicalReplace:
		return right
	default:
		return left & right
	}
}

type moveByteStep struct {
	cpu          *CPU
	programFC    uint8
	dataFC       uint8
	transactions []Transaction
}

func (c *CPU) stepMOVEByte(opcode uint16) (StepResult, error) {
	step := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		step.dataFC = 5
	}
	destinationAddress := c.addressRegister(uint8(opcode>>9&7)) & addressMask
	timed, exact := c.Bus.(ExactByteWriteBus)
	if exact && timed.HasExactByteWriteTiming(destinationAddress) &&
		opcode&0x003f == 0x003c && opcode>>6&7 == 2 {
		return c.stepMOVEByteImmediateAddressIndirect(opcode, step)
	}
	sourceMode := uint8(opcode >> 3 & 7)
	sourceReg := uint8(opcode & 7)
	destinationMode := uint8(opcode >> 6 & 7)
	destinationReg := uint8(opcode >> 9 & 7)
	if destinationMode == 1 || destinationMode == 7 && destinationReg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid MOVE.B destination mode %d:%d", destinationMode, destinationReg)
	}

	value, sourceCost, sourceMemory, err := step.readSource(sourceMode, sourceReg)
	if err != nil {
		return StepResult{}, err
	}
	destinationCost, err := step.writeDestination(destinationMode, destinationReg, value, sourceMemory)
	if err != nil {
		return StepResult{}, err
	}
	c.setLogicalFlags(uint32(value), 8)
	return StepResult{Clocks: 4 + sourceCost + destinationCost, Transactions: step.transactions}, nil
}

func (c *CPU) stepMOVEByteImmediateAddressIndirect(opcode uint16, step moveByteStep) (StepResult, error) {
	value := byte(c.State.Prefetch[1])
	extensionAddress := c.State.PC & addressMask
	extension, err := c.Bus.ReadWord(extensionAddress, step.programFC)
	if err != nil {
		return StepResult{}, err
	}
	c.State.Prefetch = [2]uint16{c.State.Prefetch[1], extension}
	c.State.PC += 2

	transactions := []Transaction{readTransaction(extensionAddress, step.programFC, extension)}
	timeline := []BusPhase{{Cycles: 4, Transaction: &transactions[0]}}
	address := c.addressRegister(uint8(opcode>>9&7)) & addressMask
	wait, err := c.Bus.(TimedBus).WriteByteAt(address, value,
		BusAccess{Clock: c.epoch + 4, FunctionCode: step.dataFC})
	if err != nil {
		return StepResult{}, err
	}
	offset := uint32(4)
	if wait != 0 {
		timeline = append(timeline, BusPhase{Offset: offset, Cycles: wait})
		offset += wait
	}
	write := writeByteTransaction(address, step.dataFC, value)
	transactions = append(transactions, write)
	timeline = append(timeline, BusPhase{Offset: offset, Cycles: 4, Transaction: &transactions[1]})
	offset += 4

	refillAddress := c.State.PC & addressMask
	refill, err := c.Bus.ReadWord(refillAddress, step.programFC)
	if err != nil {
		return StepResult{}, err
	}
	c.State.Prefetch = [2]uint16{c.State.Prefetch[1], refill}
	c.State.PC += 2
	refillTransaction := readTransaction(refillAddress, step.programFC, refill)
	transactions = append(transactions, refillTransaction)
	timeline = append(timeline, BusPhase{Offset: offset, Cycles: 4, Transaction: &transactions[2]})
	offset += 4
	c.setLogicalFlags(uint32(value), 8)
	return StepResult{Clocks: offset, Transactions: transactions, Timeline: timeline}, nil
}

func (s *moveByteStep) readSource(mode, reg uint8) (byte, uint32, bool, error) {
	c := s.cpu
	var address uint32
	readFC := s.dataFC
	var cost uint32
	if mode == 0 {
		return byte(c.State.D[reg]), 0, false, nil
	}
	if mode == 1 {
		return 0, 0, false, fmt.Errorf("m68k: MOVE.B address-register source is invalid")
	}
	switch mode {
	case 2:
		address, cost = c.addressRegister(reg), 4
	case 3:
		address, cost = c.addressRegister(reg), 4
	case 4:
		address = c.addressRegister(reg) - byteAddressDelta(reg)
		c.setAddressRegister(reg, address)
		cost = 6
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, false, err
		}
		address = c.addressRegister(reg) + uint32(int32(int16(extension)))
		cost = 8
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, false, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return 0, 0, false, err
		}
		address = c.addressRegister(reg) + index + uint32(int32(int8(extension)))
		cost = 10
	case 7:
		switch reg {
		case 0:
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, err
			}
			address, cost = uint32(int32(int16(extension))), 8
		case 1:
			high, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, err
			}
			low, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, err
			}
			address, cost = uint32(high)<<16|uint32(low), 12
		case 2:
			base := c.State.PC - 2
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, err
			}
			address = base + uint32(int32(int16(extension)))
			readFC, cost = s.programFC, 8
		case 3:
			base := c.State.PC - 2
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, err
			}
			index, err := c.briefIndex(extension)
			if err != nil {
				return 0, 0, false, err
			}
			address = base + index + uint32(int32(int8(extension)))
			readFC, cost = s.programFC, 10
		case 4:
			extension, err := s.consumeExtension()
			return byte(extension), 4, false, err
		default:
			return 0, 0, false, fmt.Errorf("m68k: invalid MOVE.B source mode %d:%d", mode, reg)
		}
	}
	value, err := c.Bus.ReadByte(address&addressMask, readFC)
	if err != nil {
		return 0, cost, true, err
	}
	s.transactions = append(s.transactions, readByteTransaction(address&addressMask, readFC, value))
	if mode == 3 {
		c.setAddressRegister(reg, c.addressRegister(reg)+byteAddressDelta(reg))
	}
	return value, cost, true, nil
}

func (s *moveByteStep) writeDestination(mode, reg uint8, value byte, sourceMemory bool) (uint32, error) {
	c := s.cpu
	if mode == 0 {
		c.State.D[reg] = c.State.D[reg]&0xffff_ff00 | uint32(value)
		return 0, s.refill()
	}
	var address uint32
	switch mode {
	case 2:
		address = c.addressRegister(reg)
		if err := s.writeByte(address, value); err != nil {
			return 0, err
		}
		return 4, s.refill()
	case 3:
		address = c.addressRegister(reg)
		if err := s.writeByte(address, value); err != nil {
			return 0, err
		}
		c.setAddressRegister(reg, c.addressRegister(reg)+byteAddressDelta(reg))
		return 4, s.refill()
	case 4:
		if err := s.refill(); err != nil {
			return 0, err
		}
		address = c.addressRegister(reg) - byteAddressDelta(reg)
		c.setAddressRegister(reg, address)
		return 4, s.writeByte(address, value)
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, err
		}
		address = c.addressRegister(reg) + uint32(int32(int16(extension)))
		if err := s.writeByte(address, value); err != nil {
			return 0, err
		}
		return 8, s.refill()
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return 0, err
		}
		address = c.addressRegister(reg) + index + uint32(int32(int8(extension)))
		if err := s.writeByte(address, value); err != nil {
			return 0, err
		}
		return 10, s.refill()
	case 7:
		if reg == 0 {
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, err
			}
			address = uint32(int32(int16(extension)))
			if err := s.writeByte(address, value); err != nil {
				return 0, err
			}
			return 8, s.refill()
		}
		if reg != 1 {
			return 0, fmt.Errorf("m68k: invalid MOVE.B destination mode %d:%d", mode, reg)
		}
		if sourceMemory {
			high := c.State.Prefetch[1]
			lowAddress := c.State.PC & addressMask
			low, err := c.Bus.ReadWord(lowAddress, s.programFC)
			if err != nil {
				return 0, err
			}
			s.transactions = append(s.transactions, readTransaction(lowAddress, s.programFC, low))
			address = uint32(high)<<16 | uint32(low)
			if err := s.writeByte(address, value); err != nil {
				return 0, err
			}
			first, err := s.readProgramWord(c.State.PC + 2)
			if err != nil {
				return 0, err
			}
			second, err := s.readProgramWord(c.State.PC + 4)
			if err != nil {
				return 0, err
			}
			c.State.Prefetch = [2]uint16{first, second}
			c.State.PC += 6
			return 12, nil
		}
		high, err := s.consumeExtension()
		if err != nil {
			return 0, err
		}
		low, err := s.consumeExtension()
		if err != nil {
			return 0, err
		}
		address = uint32(high)<<16 | uint32(low)
		if err := s.writeByte(address, value); err != nil {
			return 0, err
		}
		return 12, s.refill()
	default:
		return 0, fmt.Errorf("m68k: invalid MOVE.B destination mode %d:%d", mode, reg)
	}
}

func (s *moveByteStep) consumeExtension() (uint16, error) {
	extension := s.cpu.State.Prefetch[1]
	word, err := s.readProgramWord(s.cpu.State.PC)
	if err != nil {
		return 0, err
	}
	s.cpu.State.Prefetch[0] = extension
	s.cpu.State.Prefetch[1] = word
	s.cpu.State.PC += 2
	return extension, nil
}

func (s *moveByteStep) refill() error {
	_, err := s.consumeExtension()
	return err
}

func (s *moveByteStep) readProgramWord(address uint32) (uint16, error) {
	word, err := s.cpu.Bus.ReadWord(address&addressMask, s.programFC)
	if err == nil {
		s.transactions = append(s.transactions, readTransaction(address&addressMask, s.programFC, word))
	}
	return word, err
}

func (s *moveByteStep) writeByte(address uint32, value byte) error {
	address &= addressMask
	if err := s.cpu.Bus.WriteByte(address, value, s.dataFC); err != nil {
		return err
	}
	s.transactions = append(s.transactions, writeByteTransaction(address, s.dataFC, value))
	return nil
}

func byteAddressDelta(reg uint8) uint32 {
	if reg == 7 {
		return 2
	}
	return 1
}

type moveWordStep struct {
	moveByteStep
	opcode    uint16
	initialPC uint32
}

func (c *CPU) stepMOVEWord(opcode uint16) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	sourceMode := uint8(opcode >> 3 & 7)
	sourceReg := uint8(opcode & 7)
	destinationMode := uint8(opcode >> 6 & 7)
	destinationReg := uint8(opcode >> 9 & 7)
	if destinationMode == 1 || destinationMode == 7 && destinationReg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid MOVE.W destination mode %d:%d", destinationMode, destinationReg)
	}

	value, sourceCost, sourceMemory, fault, err := step.readWordSource(sourceMode, sourceReg)
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	destinationCost, fault, err := step.writeWordDestination(destinationMode, destinationReg, value, sourceCost, sourceMemory)
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	c.setLogicalFlags(uint32(value), 16)
	return StepResult{Clocks: 4 + sourceCost + destinationCost, Transactions: step.transactions}, nil
}

func (s *moveWordStep) readWordSource(mode, reg uint8) (uint16, uint32, bool, *StepResult, error) {
	c := s.cpu
	if mode == 0 {
		return uint16(c.State.D[reg]), 0, false, nil, nil
	}
	if mode == 1 {
		return uint16(c.addressRegister(reg)), 0, false, nil, nil
	}

	var address uint32
	var cost uint32
	readFC := s.dataFC
	switch mode {
	case 2:
		address, cost = c.addressRegister(reg), 4
	case 3:
		address, cost = c.addressRegister(reg), 4
		c.setAddressRegister(reg, address+2)
	case 4:
		address = c.addressRegister(reg) - 2
		c.setAddressRegister(reg, address)
		cost = 6
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, false, nil, err
		}
		address, cost = c.addressRegister(reg)+uint32(int32(int16(extension))), 8
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, false, nil, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return 0, 0, false, nil, err
		}
		address, cost = c.addressRegister(reg)+index+uint32(int32(int8(extension))), 10
	case 7:
		switch reg {
		case 0:
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			address, cost = uint32(int32(int16(extension))), 8
		case 1:
			high, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			low, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			address, cost = uint32(high)<<16|uint32(low), 12
		case 2, 3:
			base := c.State.PC - 2
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			address = base + uint32(int32(int16(extension)))
			cost = 8
			if reg == 3 {
				index, err := c.briefIndex(extension)
				if err != nil {
					return 0, 0, false, nil, err
				}
				address = base + index + uint32(int32(int8(extension)))
				cost = 10
			}
			readFC = s.programFC
		case 4:
			extension, err := s.consumeExtension()
			return extension, 4, false, nil, err
		default:
			return 0, 0, false, nil, fmt.Errorf("m68k: invalid MOVE.W source mode %d:%d", mode, reg)
		}
	default:
		return 0, 0, false, nil, fmt.Errorf("m68k: invalid MOVE.W source mode %d:%d", mode, reg)
	}

	if address&1 != 0 {
		result, err := c.enterAddressError(s.opcode, address, s.sourceFaultPC(mode, reg), s.transactions, 54+cost, readFC, "re", true)
		return 0, cost, true, &result, err
	}
	value, err := c.Bus.ReadWord(address&addressMask, readFC)
	if err != nil {
		var fault BusFault
		if errors.As(err, &fault) {
			faultAddress, faultFC, write, size := fault.M68KBusFault()
			if !write && size == 2 && faultAddress == address&addressMask {
				result, faultErr := c.enterBusError(s.opcode, address, s.sourceFaultPC(mode, reg), s.transactions, 60+cost, faultFC, "re", true)
				return 0, cost, true, &result, faultErr
			}
		}
		return 0, 0, true, nil, err
	}
	s.transactions = append(s.transactions, readTransaction(address&addressMask, readFC, value))
	return value, cost, true, nil, nil
}

func (s *moveWordStep) sourceFaultPC(mode, reg uint8) uint32 {
	switch mode {
	case 2, 3, 5, 6:
		return s.initialPC - 2
	case 4:
		return s.initialPC
	case 7:
		switch reg {
		case 0:
			return s.initialPC
		case 1:
			return s.initialPC + 2
		default:
			return s.initialPC - 2
		}
	}
	return s.initialPC
}

func (s *moveWordStep) writeWordDestination(mode, reg uint8, value uint16, sourceCost uint32, sourceMemory bool) (uint32, *StepResult, error) {
	c := s.cpu
	if mode == 0 {
		c.State.D[reg] = c.State.D[reg]&0xffff_0000 | uint32(value)
		return 0, nil, s.refill()
	}

	savedPC := c.State.PC
	var address uint32
	var cost, faultExtra uint32
	refillBeforeWrite := false
	switch mode {
	case 2:
		address, cost = c.addressRegister(reg), 4
	case 3:
		address, cost = c.addressRegister(reg), 4
	case 4:
		address, cost, faultExtra = c.addressRegister(reg)-2, 4, 4
		c.setAddressRegister(reg, address)
		refillBeforeWrite = true
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		address, cost, faultExtra = c.addressRegister(reg)+uint32(int32(int16(extension))), 8, 4
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return 0, nil, err
		}
		address, cost, faultExtra = c.addressRegister(reg)+index+uint32(int32(int8(extension))), 10, 6
	case 7:
		if reg == 0 {
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, nil, err
			}
			address, cost, faultExtra = uint32(int32(int16(extension))), 8, 4
			break
		}
		if reg != 1 {
			return 0, nil, fmt.Errorf("m68k: invalid MOVE.W destination mode %d:%d", mode, reg)
		}
		cost, faultExtra = 12, 8
		if sourceMemory {
			faultExtra = 4
			high := c.State.Prefetch[1]
			lowAddress := c.State.PC & addressMask
			low, err := c.Bus.ReadWord(lowAddress, s.programFC)
			if err != nil {
				return 0, nil, err
			}
			s.transactions = append(s.transactions, readTransaction(lowAddress, s.programFC, low))
			address = uint32(high)<<16 | uint32(low)
			if address&1 != 0 {
				return s.wordWriteFault(s.opcode, address, savedPC, value, sourceCost, faultExtra)
			}
			if err := s.writeWord(address, value); err != nil {
				return 0, nil, err
			}
			first, err := s.readProgramWord(c.State.PC + 2)
			if err != nil {
				return 0, nil, err
			}
			second, err := s.readProgramWord(c.State.PC + 4)
			if err != nil {
				return 0, nil, err
			}
			c.State.Prefetch = [2]uint16{first, second}
			c.State.PC += 6
			return cost, nil, nil
		}
		high, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		low, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		address = uint32(high)<<16 | uint32(low)
		savedPC += 2
	default:
		return 0, nil, fmt.Errorf("m68k: invalid MOVE.W destination mode %d:%d", mode, reg)
	}

	if refillBeforeWrite {
		if err := s.refill(); err != nil {
			return 0, nil, err
		}
	}
	if address&1 != 0 {
		faultOpcode := s.opcode
		if refillBeforeWrite {
			faultOpcode = c.State.Prefetch[0]
		}
		return s.wordWriteFault(faultOpcode, address, savedPC, value, sourceCost, faultExtra)
	}
	if err := s.writeWord(address, value); err != nil {
		return 0, nil, err
	}
	if mode == 3 {
		c.setAddressRegister(reg, address+2)
	}
	if refillBeforeWrite {
		return cost, nil, nil
	}
	return cost, nil, s.refill()
}

func (s *moveWordStep) wordWriteFault(opcode uint16, address, savedPC uint32, value uint16, sourceCost, faultExtra uint32) (uint32, *StepResult, error) {
	s.cpu.setLogicalFlags(uint32(value), 16)
	result, err := s.cpu.enterAddressError(opcode, address, savedPC, s.transactions, 58+sourceCost+faultExtra, s.dataFC, "we", false)
	return 0, &result, err
}

func (s *moveWordStep) writeWord(address uint32, value uint16) error {
	address &= addressMask
	if err := s.cpu.Bus.WriteWord(address, value, s.dataFC); err != nil {
		return err
	}
	s.transactions = append(s.transactions, writeTransaction(address, s.dataFC, value))
	return nil
}

type moveLongStep struct {
	moveByteStep
	opcode    uint16
	initialPC uint32
}

func (c *CPU) stepMOVELong(opcode uint16) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	sourceMode := uint8(opcode >> 3 & 7)
	sourceReg := uint8(opcode & 7)
	destinationMode := uint8(opcode >> 6 & 7)
	destinationReg := uint8(opcode >> 9 & 7)
	if destinationMode == 1 || destinationMode == 7 && destinationReg > 1 {
		return StepResult{}, fmt.Errorf("m68k: invalid MOVE.L destination mode %d:%d", destinationMode, destinationReg)
	}
	if sourceMode == 7 && sourceReg == 4 && destinationMode == 7 && destinationReg == 0 {
		return c.stepMOVELongImmediateAbsoluteShort(opcode, stream)
	}

	value, sourceCost, sourceMemory, fault, err := step.readLongSource(sourceMode, sourceReg)
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	destinationCost, fault, err := step.writeLongDestination(destinationMode, destinationReg, value, sourceCost, sourceMemory)
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	c.setLogicalFlags(value, 32)
	return StepResult{Clocks: 4 + sourceCost + destinationCost, Transactions: step.transactions}, nil
}

func (c *CPU) stepMOVELongImmediateAbsoluteShort(opcode uint16, stream moveByteStep) (StepResult, error) {
	var transactions []Transaction
	var timeline []BusPhase
	var offset uint32

	appendWait := func(wait uint32) {
		if wait != 0 {
			timeline = append(timeline, BusPhase{Offset: offset, Cycles: wait})
			offset += wait
		}
	}
	readWord := func(address uint32, fc uint8) (uint16, error) {
		word, wait, err := c.readWordAt(address&addressMask, fc, offset)
		if err != nil {
			return 0, err
		}
		appendWait(wait)
		transaction := readTransaction(address&addressMask, fc, word)
		transactions = append(transactions, transaction)
		timeline = append(timeline, BusPhase{Offset: offset, Cycles: 4, Transaction: &transaction})
		offset += 4
		return word, nil
	}
	consumeExtension := func() (uint16, error) {
		extension := c.State.Prefetch[1]
		word, err := readWord(c.State.PC, stream.programFC)
		if err != nil {
			return 0, err
		}
		c.State.Prefetch = [2]uint16{extension, word}
		c.State.PC += 2
		return extension, nil
	}
	writeWord := func(address uint32, value uint16) error {
		wait, err := c.writeWordAt(address&addressMask, value, stream.dataFC, offset)
		if err != nil {
			return err
		}
		appendWait(wait)
		transaction := writeTransaction(address&addressMask, stream.dataFC, value)
		transactions = append(transactions, transaction)
		timeline = append(timeline, BusPhase{Offset: offset, Cycles: 4, Transaction: &transaction})
		offset += 4
		return nil
	}

	high, err := consumeExtension()
	if err != nil {
		return StepResult{}, err
	}
	low, err := consumeExtension()
	if err != nil {
		return StepResult{}, err
	}
	destination, err := consumeExtension()
	if err != nil {
		return StepResult{}, err
	}
	address := uint32(int32(int16(destination)))
	value := uint32(high)<<16 | uint32(low)
	if address&1 != 0 {
		result, faultErr := c.enterAddressError(opcode, address, c.State.PC-2, transactions,
			70+offset-12, stream.dataFC, "we", false)
		return result, faultErr
	}
	if err := writeWord(address, high); err != nil {
		return StepResult{}, err
	}
	if err := writeWord(address+2, low); err != nil {
		return StepResult{}, err
	}
	if _, err := consumeExtension(); err != nil {
		return StepResult{}, err
	}
	c.setLogicalFlags(value, 32)
	return StepResult{Clocks: offset, Transactions: transactions, Timeline: timeline}, nil
}

func (s *moveLongStep) readLongSource(mode, reg uint8) (uint32, uint32, bool, *StepResult, error) {
	c := s.cpu
	if mode == 0 {
		return c.State.D[reg], 0, false, nil, nil
	}
	if mode == 1 {
		return c.addressRegister(reg), 0, false, nil, nil
	}

	var address uint32
	var cost, faultCost uint32
	readFC := s.dataFC
	switch mode {
	case 2:
		address, cost, faultCost = c.addressRegister(reg), 8, 4
	case 3:
		address, cost, faultCost = c.addressRegister(reg), 8, 4
	case 4:
		address = c.addressRegister(reg) - 4
		c.setAddressRegister(reg, address)
		cost, faultCost = 10, 6
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, false, nil, err
		}
		address = c.addressRegister(reg) + uint32(int32(int16(extension)))
		cost, faultCost = 12, 8
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, 0, false, nil, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return 0, 0, false, nil, err
		}
		address = c.addressRegister(reg) + index + uint32(int32(int8(extension)))
		cost, faultCost = 14, 10
	case 7:
		switch reg {
		case 0:
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			address, cost, faultCost = uint32(int32(int16(extension))), 12, 8
		case 1:
			high, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			low, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			address, cost, faultCost = uint32(high)<<16|uint32(low), 16, 12
		case 2, 3:
			base := c.State.PC - 2
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			address = base + uint32(int32(int16(extension)))
			cost, faultCost = 12, 8
			if reg == 3 {
				index, err := c.briefIndex(extension)
				if err != nil {
					return 0, 0, false, nil, err
				}
				address = base + index + uint32(int32(int8(extension)))
				cost, faultCost = 14, 10
			}
			readFC = s.programFC
		case 4:
			high, err := s.consumeExtension()
			if err != nil {
				return 0, 0, false, nil, err
			}
			low, err := s.consumeExtension()
			return uint32(high)<<16 | uint32(low), 8, false, nil, err
		default:
			return 0, 0, false, nil, fmt.Errorf("m68k: invalid MOVE.L source mode %d:%d", mode, reg)
		}
	default:
		return 0, 0, false, nil, fmt.Errorf("m68k: invalid MOVE.L source mode %d:%d", mode, reg)
	}

	if address&1 != 0 {
		result, err := c.enterAddressError(s.opcode, address, s.sourceFaultPC(mode, reg), s.transactions, 54+faultCost, readFC, "re", true)
		return 0, cost, true, &result, err
	}
	high, err := s.readLongWord(address, readFC)
	if err != nil {
		return 0, 0, true, nil, err
	}
	low, err := s.readLongWord(address+2, readFC)
	if err != nil {
		return 0, 0, true, nil, err
	}
	if mode == 3 {
		c.setAddressRegister(reg, address+4)
	}
	return uint32(high)<<16 | uint32(low), cost, true, nil, nil
}

func (s *moveLongStep) sourceFaultPC(mode, reg uint8) uint32 {
	switch mode {
	case 2, 3, 4, 5, 6:
		return s.initialPC - 2
	case 7:
		switch reg {
		case 0:
			return s.initialPC
		case 1:
			return s.initialPC + 2
		default:
			return s.initialPC - 2
		}
	}
	return s.initialPC
}

func (s *moveLongStep) readLongWord(address uint32, fc uint8) (uint16, error) {
	word, err := s.cpu.Bus.ReadWord(address&addressMask, fc)
	if err == nil {
		s.transactions = append(s.transactions, readTransaction(address&addressMask, fc, word))
	}
	return word, err
}

func (s *moveLongStep) writeLongDestination(mode, reg uint8, value uint32, sourceCost uint32, sourceMemory bool) (uint32, *StepResult, error) {
	c := s.cpu
	if mode == 0 {
		c.State.D[reg] = value
		return 0, nil, s.refill()
	}

	savedPC := c.State.PC
	var address uint32
	var cost, faultExtra uint32
	refillBeforeWrite := false
	switch mode {
	case 2:
		address, cost = c.addressRegister(reg), 8
	case 3:
		address, cost = c.addressRegister(reg), 8
	case 4:
		address, cost, faultExtra = c.addressRegister(reg)-2, 8, 4
		refillBeforeWrite = true
	case 5:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		address, cost, faultExtra = c.addressRegister(reg)+uint32(int32(int16(extension))), 12, 4
	case 6:
		extension, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		index, err := c.briefIndex(extension)
		if err != nil {
			return 0, nil, err
		}
		address, cost, faultExtra = c.addressRegister(reg)+index+uint32(int32(int8(extension))), 14, 6
	case 7:
		if reg == 0 {
			extension, err := s.consumeExtension()
			if err != nil {
				return 0, nil, err
			}
			address, cost, faultExtra = uint32(int32(int16(extension))), 12, 4
			break
		}
		if reg != 1 {
			return 0, nil, fmt.Errorf("m68k: invalid MOVE.L destination mode %d:%d", mode, reg)
		}
		cost, faultExtra = 16, 8
		if sourceMemory {
			faultExtra = 4
			high := c.State.Prefetch[1]
			lowAddress := c.State.PC & addressMask
			low, err := c.Bus.ReadWord(lowAddress, s.programFC)
			if err != nil {
				return 0, nil, err
			}
			s.transactions = append(s.transactions, readTransaction(lowAddress, s.programFC, low))
			address = uint32(high)<<16 | uint32(low)
			if address&1 != 0 {
				return s.longWriteFault(mode, reg, address, savedPC, value, sourceCost, faultExtra, sourceMemory)
			}
			if err := s.writeLong(address, value); err != nil {
				return 0, nil, err
			}
			first, err := s.readProgramWord(c.State.PC + 2)
			if err != nil {
				return 0, nil, err
			}
			second, err := s.readProgramWord(c.State.PC + 4)
			if err != nil {
				return 0, nil, err
			}
			c.State.Prefetch = [2]uint16{first, second}
			c.State.PC += 6
			return cost, nil, nil
		}
		high, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		low, err := s.consumeExtension()
		if err != nil {
			return 0, nil, err
		}
		address = uint32(high)<<16 | uint32(low)
		savedPC += 2
	default:
		return 0, nil, fmt.Errorf("m68k: invalid MOVE.L destination mode %d:%d", mode, reg)
	}

	if refillBeforeWrite {
		if err := s.refill(); err != nil {
			return 0, nil, err
		}
	}
	if address&1 != 0 {
		return s.longWriteFault(mode, reg, address, savedPC, value, sourceCost, faultExtra, sourceMemory)
	}
	if mode == 4 {
		address -= 2
		c.setAddressRegister(reg, address)
	}
	var err error
	if mode == 4 {
		err = s.writeLongPredecrement(address, value)
	} else {
		err = s.writeLong(address, value)
	}
	if err != nil {
		return 0, nil, err
	}
	if mode == 3 {
		c.setAddressRegister(reg, address+4)
	}
	if refillBeforeWrite {
		return cost, nil, nil
	}
	return cost, nil, s.refill()
}

func (s *moveLongStep) longWriteFault(mode, reg uint8, address, savedPC uint32, value uint32, sourceCost, faultExtra uint32, sourceMemory bool) (uint32, *StepResult, error) {
	if sourceMemory {
		if mode == 2 || mode == 3 || mode == 7 && reg == 1 {
			s.cpu.setLogicalFlags(uint32(uint16(value)), 16)
		} else {
			s.cpu.setLogicalFlags(value, 32)
		}
	} else {
		switch mode {
		case 4, 7:
			s.cpu.setLogicalFlags(value, 32)
		case 5, 6:
			s.cpu.State.SR &^= 0x000c
			if value == 0 {
				s.cpu.State.SR |= 0x0004
			}
			if value&0x8000_0000 != 0 {
				s.cpu.State.SR |= 0x0008
			}
		}
	}
	result, err := s.cpu.enterAddressError(s.opcode, address, savedPC, s.transactions, 58+sourceCost+faultExtra, s.dataFC, "we", false)
	return 0, &result, err
}

func (s *moveLongStep) writeLong(address, value uint32) error {
	if err := s.writeLongWord(address, uint16(value>>16)); err != nil {
		return err
	}
	return s.writeLongWord(address+2, uint16(value))
}

func (s *moveLongStep) writeLongPredecrement(address, value uint32) error {
	if err := s.writeLongWord(address+2, uint16(value)); err != nil {
		return err
	}
	return s.writeLongWord(address, uint16(value>>16))
}

func (s *moveLongStep) writeLongWord(address uint32, value uint16) error {
	address &= addressMask
	if err := s.cpu.Bus.WriteWord(address, value, s.dataFC); err != nil {
		return err
	}
	s.transactions = append(s.transactions, writeTransaction(address, s.dataFC, value))
	return nil
}

func (c *CPU) stepMOVEAWord(opcode uint16) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	value, sourceCost, _, fault, err := step.readWordSource(uint8(opcode>>3&7), uint8(opcode&7))
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	c.setAddressRegister(uint8(opcode>>9&7), uint32(int32(int16(value))))
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 4 + sourceCost, Transactions: step.transactions}, nil
}

func (c *CPU) stepMOVEALong(opcode uint16) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	value, sourceCost, _, fault, err := step.readLongSource(uint8(opcode>>3&7), uint8(opcode&7))
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	c.setAddressRegister(uint8(opcode>>9&7), value)
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 4 + sourceCost, Transactions: step.transactions}, nil
}

func (c *CPU) stepADDAWord(opcode uint16) (StepResult, error) {
	return c.stepAddressArithmeticWord(opcode, false)
}

func (c *CPU) stepSUBAWord(opcode uint16) (StepResult, error) {
	return c.stepAddressArithmeticWord(opcode, true)
}

func (c *CPU) stepAddressArithmeticWord(opcode uint16, subtract bool) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveWordStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	value, sourceCost, _, fault, err := step.readWordSource(uint8(opcode>>3&7), uint8(opcode&7))
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	destination := uint8(opcode >> 9 & 7)
	c.setAddressRegister(destination, arithmeticLong(c.addressRegister(destination), uint32(int32(int16(value))), subtract))
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	return StepResult{Clocks: 8 + sourceCost, Transactions: step.transactions}, nil
}

func (c *CPU) stepADDALong(opcode uint16) (StepResult, error) {
	return c.stepAddressArithmeticLong(opcode, false)
}

func (c *CPU) stepSUBALong(opcode uint16) (StepResult, error) {
	return c.stepAddressArithmeticLong(opcode, true)
}

func (c *CPU) stepAddressArithmeticLong(opcode uint16, subtract bool) (StepResult, error) {
	stream := moveByteStep{cpu: c, programFC: c.programFunctionCode(), dataFC: 1}
	if c.State.SR&supervisor != 0 {
		stream.dataFC = 5
	}
	step := moveLongStep{moveByteStep: stream, opcode: opcode, initialPC: c.State.PC}
	value, sourceCost, sourceMemory, fault, err := step.readLongSource(uint8(opcode>>3&7), uint8(opcode&7))
	if err != nil {
		return StepResult{}, err
	}
	if fault != nil {
		return *fault, nil
	}
	destination := uint8(opcode >> 9 & 7)
	c.setAddressRegister(destination, arithmeticLong(c.addressRegister(destination), value, subtract))
	if err := step.refill(); err != nil {
		return StepResult{}, err
	}
	clocks := uint32(6) + sourceCost
	if !sourceMemory {
		clocks += 2
	}
	return StepResult{Clocks: clocks, Transactions: step.transactions}, nil
}

type controlEA struct {
	target         uint32
	returnPC       uint32
	extraClocks    uint32
	extensionWords uint8
	indexed        bool
	transactions   []Transaction
}

func (c *CPU) decodeControlEA(opcode uint16) (controlEA, error) {
	mode := uint8(opcode >> 3 & 7)
	reg := uint8(opcode & 7)
	basePC := c.State.PC - 2
	result := controlEA{returnPC: basePC}
	switch mode {
	case 2:
		result.target = c.addressRegister(reg)
	case 5:
		result.target = c.addressRegister(reg) + uint32(int32(int16(c.State.Prefetch[1])))
		result.returnPC = c.State.PC
		result.extraClocks = 2
		result.extensionWords = 1
	case 6:
		index, err := c.briefIndex(c.State.Prefetch[1])
		if err != nil {
			return controlEA{}, err
		}
		result.target = c.addressRegister(reg) + index + uint32(int32(int8(c.State.Prefetch[1])))
		result.returnPC = c.State.PC
		result.extraClocks = 6
		result.extensionWords = 1
		result.indexed = true
	case 7:
		switch reg {
		case 0:
			result.target = uint32(int32(int16(c.State.Prefetch[1])))
			result.returnPC = c.State.PC
			result.extraClocks = 2
			result.extensionWords = 1
		case 1:
			address := c.State.PC & addressMask
			fc := c.programFunctionCode()
			low, err := c.Bus.ReadWord(address, fc)
			if err != nil {
				return controlEA{}, err
			}
			result.target = uint32(c.State.Prefetch[1])<<16 | uint32(low)
			result.returnPC = c.State.PC + 2
			result.extraClocks = 4
			result.extensionWords = 2
			result.transactions = []Transaction{readTransaction(address, fc, low)}
		case 2:
			result.target = basePC + uint32(int32(int16(c.State.Prefetch[1])))
			result.returnPC = c.State.PC
			result.extraClocks = 2
			result.extensionWords = 1
		case 3:
			index, err := c.briefIndex(c.State.Prefetch[1])
			if err != nil {
				return controlEA{}, err
			}
			result.target = basePC + index + uint32(int32(int8(c.State.Prefetch[1])))
			result.returnPC = c.State.PC
			result.extraClocks = 6
			result.extensionWords = 1
			result.indexed = true
		default:
			return controlEA{}, fmt.Errorf("m68k: invalid control addressing mode %d:%d", mode, reg)
		}
	default:
		return controlEA{}, fmt.Errorf("m68k: invalid control addressing mode %d:%d", mode, reg)
	}
	return result, nil
}

func isControlMode(opcode uint16) bool {
	mode := uint8(opcode >> 3 & 7)
	reg := uint8(opcode & 7)
	return mode == 2 || mode == 5 || mode == 6 || mode == 7 && reg <= 3
}

func (c *CPU) stepLEA(opcode uint16) (StepResult, error) {
	ea, err := c.decodeControlEA(opcode)
	if err != nil {
		return StepResult{}, err
	}
	destination := uint8(opcode >> 9 & 7)
	c.setAddressRegister(destination, ea.target)
	clocks := uint32(4 + 4*ea.extensionWords)
	if ea.indexed {
		clocks += 4
	}
	return c.refillSequential(ea, clocks)
}

func (c *CPU) stepPEA(opcode uint16) (StepResult, error) {
	ea, err := c.decodeControlEA(opcode)
	if err != nil {
		return StepResult{}, err
	}
	programFC := c.programFunctionCode()
	transactions := append([]Transaction{}, ea.transactions...)
	clocks := uint32(12 + 4*ea.extensionWords)
	if ea.indexed {
		clocks += 4
	}

	if ea.extensionWords == 0 {
		address := c.State.PC & addressMask
		word, err := c.Bus.ReadWord(address, programFC)
		if err != nil {
			return StepResult{}, err
		}
		transactions = append(transactions, readTransaction(address, programFC, word))
		stackTransactions, err := c.pushLong(ea.target)
		if err != nil {
			return StepResult{}, err
		}
		transactions = append(transactions, stackTransactions...)
		c.State.Prefetch[0] = c.State.Prefetch[1]
		c.State.Prefetch[1] = word
		c.State.PC += 2
		return StepResult{Clocks: clocks, Transactions: transactions}, nil
	}

	start := c.State.PC + uint32(ea.extensionWords-1)*2
	firstAddress := start & addressMask
	first, err := c.Bus.ReadWord(firstAddress, programFC)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions, readTransaction(firstAddress, programFC, first))
	absolute := opcode>>3&7 == 7 && opcode&7 <= 1
	if absolute {
		stackTransactions, err := c.pushLong(ea.target)
		if err != nil {
			return StepResult{}, err
		}
		transactions = append(transactions, stackTransactions...)
	}
	secondAddress := (start + 2) & addressMask
	second, err := c.Bus.ReadWord(secondAddress, programFC)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions, readTransaction(secondAddress, programFC, second))
	if !absolute {
		stackTransactions, err := c.pushLong(ea.target)
		if err != nil {
			return StepResult{}, err
		}
		transactions = append(transactions, stackTransactions...)
	}
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = start + 4
	return StepResult{Clocks: clocks, Transactions: transactions}, nil
}

func (c *CPU) refillSequential(ea controlEA, clocks uint32) (StepResult, error) {
	fc := c.programFunctionCode()
	transactions := append([]Transaction{}, ea.transactions...)
	if ea.extensionWords == 0 {
		address := c.State.PC & addressMask
		word, err := c.Bus.ReadWord(address, fc)
		if err != nil {
			return StepResult{}, err
		}
		transactions = append(transactions, readTransaction(address, fc, word))
		c.State.Prefetch[0] = c.State.Prefetch[1]
		c.State.Prefetch[1] = word
		c.State.PC += 2
		return StepResult{Clocks: clocks, Transactions: transactions}, nil
	}
	start := c.State.PC + uint32(ea.extensionWords-1)*2
	firstAddress := start & addressMask
	first, err := c.Bus.ReadWord(firstAddress, fc)
	if err != nil {
		return StepResult{}, err
	}
	secondAddress := (start + 2) & addressMask
	second, err := c.Bus.ReadWord(secondAddress, fc)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions,
		readTransaction(firstAddress, fc, first),
		readTransaction(secondAddress, fc, second))
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = start + 4
	return StepResult{Clocks: clocks, Transactions: transactions}, nil
}

func (c *CPU) pushLong(value uint32) ([]Transaction, error) {
	stack := &c.State.USP
	dataFC := uint8(1)
	if c.State.SR&supervisor != 0 {
		stack = &c.State.SSP
		dataFC = 5
	}
	newSP := *stack - 4
	firstAddress := newSP & addressMask
	if err := c.Bus.WriteWord(firstAddress, uint16(value>>16), dataFC); err != nil {
		return nil, err
	}
	secondAddress := (newSP + 2) & addressMask
	if err := c.Bus.WriteWord(secondAddress, uint16(value), dataFC); err != nil {
		return nil, err
	}
	*stack = newSP
	return []Transaction{
		writeTransaction(firstAddress, dataFC, uint16(value>>16)),
		writeTransaction(secondAddress, dataFC, uint16(value)),
	}, nil
}

func (c *CPU) setAddressRegister(reg uint8, value uint32) {
	if reg < 7 {
		c.State.A[reg] = value
		return
	}
	if c.State.SR&supervisor != 0 {
		c.State.SSP = value
		return
	}
	c.State.USP = value
}

func (c *CPU) briefIndex(extension uint16) (uint32, error) {
	reg := uint8(extension >> 12 & 7)
	value := c.State.D[reg]
	if extension&0x8000 != 0 {
		value = c.addressRegister(reg)
	}
	if extension&0x0800 == 0 {
		value = uint32(int32(int16(value)))
	}
	return value, nil
}

func (c *CPU) addressRegister(reg uint8) uint32 {
	if reg < 7 {
		return c.State.A[reg]
	}
	if c.State.SR&supervisor != 0 {
		return c.State.SSP
	}
	return c.State.USP
}

func (c *CPU) stepJMP(opcode uint16) (StepResult, error) {
	ea, err := c.decodeControlEA(opcode)
	if err != nil {
		return StepResult{}, err
	}
	if ea.target&1 != 0 {
		return c.enterInstructionAddressError(
			opcode, ea.target, c.State.PC-2, ea.transactions, 58+ea.extraClocks,
		)
	}
	result, err := c.refillBranch(ea.target, 8+ea.extraClocks)
	if err != nil {
		return StepResult{}, err
	}
	result.Transactions = append(ea.transactions, result.Transactions...)
	return result, nil
}

func (c *CPU) stepJSR(opcode uint16) (StepResult, error) {
	ea, err := c.decodeControlEA(opcode)
	if err != nil {
		return StepResult{}, err
	}
	if ea.target&1 != 0 {
		return c.enterInstructionAddressError(
			opcode, ea.target, ea.returnPC, ea.transactions, 58+ea.extraClocks,
		)
	}

	programFC := c.programFunctionCode()
	targetAddress := ea.target & addressMask
	first, err := c.Bus.ReadWord(targetAddress, programFC)
	if err != nil {
		return StepResult{}, err
	}
	transactions := append(ea.transactions, readTransaction(targetAddress, programFC, first))
	stack := &c.State.USP
	dataFC := uint8(1)
	if c.State.SR&supervisor != 0 {
		stack = &c.State.SSP
		dataFC = 5
	}
	newSP := *stack - 4
	returnHigh := uint16(ea.returnPC >> 16)
	returnLow := uint16(ea.returnPC)
	stackAddress := newSP & addressMask
	if err := c.Bus.WriteWord(stackAddress, returnHigh, dataFC); err != nil {
		return StepResult{}, err
	}
	stackLowAddress := (newSP + 2) & addressMask
	if err := c.Bus.WriteWord(stackLowAddress, returnLow, dataFC); err != nil {
		return StepResult{}, err
	}
	*stack = newSP
	transactions = append(transactions,
		writeTransaction(stackAddress, dataFC, returnHigh),
		writeTransaction(stackLowAddress, dataFC, returnLow))
	secondAddress := (ea.target + 2) & addressMask
	second, err := c.Bus.ReadWord(secondAddress, programFC)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions, readTransaction(secondAddress, programFC, second))
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = ea.target + 4
	return StepResult{Clocks: 16 + ea.extraClocks, Transactions: transactions}, nil
}

func (c *CPU) stepRTS(opcode uint16) (StepResult, error) {
	stack := &c.State.USP
	dataFC := uint8(1)
	if c.State.SR&supervisor != 0 {
		stack = &c.State.SSP
		dataFC = 5
	}
	stackAddress := *stack & addressMask
	returnHigh, err := c.Bus.ReadWord(stackAddress, dataFC)
	if err != nil {
		return StepResult{}, err
	}
	secondAddress := (*stack + 2) & addressMask
	returnLow, err := c.Bus.ReadWord(secondAddress, dataFC)
	if err != nil {
		return StepResult{}, err
	}
	*stack += 4
	returnPC := uint32(returnHigh)<<16 | uint32(returnLow)
	prefix := []Transaction{
		readTransaction(stackAddress, dataFC, returnHigh),
		readTransaction(secondAddress, dataFC, returnLow),
	}
	if returnPC&1 != 0 {
		return c.enterInstructionAddressError(opcode, returnPC, c.State.PC-2, prefix, 66)
	}
	result, err := c.refillBranch(returnPC, 16)
	if err != nil {
		return StepResult{}, err
	}
	result.Transactions = append(prefix, result.Transactions...)
	return result, nil
}

func (c *CPU) stepRTE(opcode uint16) (StepResult, error) {
	if c.State.SR&supervisor == 0 {
		return c.enterStandardException(8, c.State.PC-4, nil, 34)
	}
	frame := c.State.SSP
	restoredSR, err := c.Bus.ReadWord(frame&addressMask, 5)
	if err != nil {
		return StepResult{}, err
	}
	high, err := c.Bus.ReadWord((frame+2)&addressMask, 5)
	if err != nil {
		return StepResult{}, err
	}
	low, err := c.Bus.ReadWord((frame+4)&addressMask, 5)
	if err != nil {
		return StepResult{}, err
	}
	transactions := []Transaction{
		readTransaction(frame&addressMask, 5, restoredSR),
		readTransaction((frame+2)&addressMask, 5, high),
		readTransaction((frame+4)&addressMask, 5, low),
	}
	target := uint32(high)<<16 | uint32(low)
	c.State.SSP = frame + 6
	c.State.SR = restoredSR & 0xa71f
	if target&1 != 0 {
		return c.enterAddressError(opcode, target, c.State.PC-2, transactions, 70,
			c.programFunctionCode(), "re", true)
	}
	fc := c.programFunctionCode()
	first, err := c.Bus.ReadWord(target&addressMask, fc)
	if err != nil {
		return StepResult{}, err
	}
	second, err := c.Bus.ReadWord((target+2)&addressMask, fc)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions,
		readTransaction(target&addressMask, fc, first),
		readTransaction((target+2)&addressMask, fc, second))
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = target + 4
	return StepResult{Clocks: 20, Transactions: transactions}, nil
}

func (c *CPU) stepBSR(opcode uint16) (StepResult, error) {
	displacement8 := uint8(opcode)
	base := c.State.PC - 2
	returnPC := base
	var displacement int32
	if displacement8 == 0 {
		displacement = int32(int16(c.State.Prefetch[1]))
		returnPC = c.State.PC
	} else {
		displacement = int32(int8(displacement8))
	}
	target := uint32(int32(base) + displacement)

	stack := &c.State.USP
	dataFC := uint8(1)
	if c.State.SR&supervisor != 0 {
		stack = &c.State.SSP
		dataFC = 5
	}
	newSP := *stack - 4
	returnHigh := uint16(returnPC >> 16)
	returnLow := uint16(returnPC)
	firstAddress := newSP & addressMask
	if err := c.Bus.WriteWord(firstAddress, returnHigh, dataFC); err != nil {
		return StepResult{}, err
	}
	secondAddress := (newSP + 2) & addressMask
	if err := c.Bus.WriteWord(secondAddress, returnLow, dataFC); err != nil {
		return StepResult{}, err
	}
	*stack = newSP
	prefix := []Transaction{
		writeTransaction(firstAddress, dataFC, returnHigh),
		writeTransaction(secondAddress, dataFC, returnLow),
	}

	if target&1 != 0 {
		return c.enterInstructionAddressError(opcode, target, target, prefix, 68)
	}
	result, err := c.refillBranch(target, 18)
	if err != nil {
		return StepResult{}, err
	}
	result.Transactions = append(prefix, result.Transactions...)
	return result, nil
}

func (c *CPU) stepBranch(opcode uint16) (StepResult, error) {
	condition := uint8(opcode >> 8 & 0x0f)
	taken := condition == 0 || branchCondition(condition, c.State.SR)
	displacement8 := uint8(opcode)
	base := c.State.PC - 2

	if taken {
		var displacement int32
		if displacement8 == 0 {
			displacement = int32(int16(c.State.Prefetch[1]))
		} else {
			displacement = int32(int8(displacement8))
		}
		target := uint32(int32(base) + displacement)
		if target&1 != 0 {
			return c.enterInstructionAddressError(opcode, target, base, nil, 60)
		}
		return c.refillBranch(target, 10)
	}

	if displacement8 != 0 {
		address := c.State.PC & addressMask
		fc := c.programFunctionCode()
		word, err := c.Bus.ReadWord(address, fc)
		if err != nil {
			return StepResult{}, err
		}
		c.State.Prefetch[0] = c.State.Prefetch[1]
		c.State.Prefetch[1] = word
		c.State.PC += 2
		return StepResult{Clocks: 8, Transactions: []Transaction{
			readTransaction(address, fc, word),
		}}, nil
	}

	return c.refillBranch(c.State.PC, 12)
}

func (c *CPU) enterInstructionAddressError(
	opcode uint16,
	target uint32,
	savedPC uint32,
	prefix []Transaction,
	clocks uint32,
) (StepResult, error) {
	return c.enterAddressError(opcode, target, savedPC, prefix, clocks, c.programFunctionCode(), "re", true)
}

func (c *CPU) enterAddressError(
	opcode uint16,
	target uint32,
	savedPC uint32,
	prefix []Transaction,
	clocks uint32,
	faultFC uint8,
	faultKind string,
	read bool,
) (StepResult, error) {
	return c.enterAccessError(opcode, target, savedPC, prefix, clocks, faultFC, faultKind, read, 0x00000c, 2)
}

func (c *CPU) enterBusError(
	opcode uint16,
	target uint32,
	savedPC uint32,
	prefix []Transaction,
	clocks uint32,
	faultFC uint8,
	faultKind string,
	read bool,
) (StepResult, error) {
	return c.enterAccessError(opcode, target, savedPC, prefix, clocks, faultFC, faultKind, read, 0x000008, 2)
}

func (c *CPU) enterByteBusError(
	opcode uint16,
	target uint32,
	savedPC uint32,
	prefix []Transaction,
	clocks uint32,
	faultFC uint8,
	faultKind string,
	read bool,
) (StepResult, error) {
	return c.enterAccessError(opcode, target, savedPC, prefix, clocks, faultFC, faultKind, read, 0x000008, 1)
}

func (c *CPU) enterAccessError(
	opcode uint16,
	target uint32,
	savedPC uint32,
	prefix []Transaction,
	clocks uint32,
	faultFC uint8,
	faultKind string,
	read bool,
	vectorAddress uint32,
	faultSize uint8,
) (StepResult, error) {
	originalSR := c.State.SR
	faultBusAddress := target & addressMask &^ 1

	newSP := c.State.SSP - 14
	ssw := opcode&0xffe0 | uint16(faultFC)
	if read {
		ssw |= 0x0010
	}
	writes := []struct {
		address uint32
		value   uint16
	}{
		{newSP + 12, uint16(savedPC)},
		{newSP + 8, originalSR},
		{newSP + 10, uint16(savedPC >> 16)},
		{newSP + 6, opcode},
		{newSP + 4, uint16(target)},
		{newSP, ssw},
		{newSP + 2, uint16(target >> 16)},
	}
	uds, lds := true, true
	if faultSize == 1 {
		uds = target&1 == 0
		lds = !uds
	}
	transactions := append(prefix, Transaction{
		Kind: faultKind, Cycle: 4, FC: faultFC, Address: faultBusAddress,
		Size: faultSize, Data: 0, UDS: uds, LDS: lds,
	})
	for _, write := range writes {
		address := write.address & addressMask
		if err := c.Bus.WriteWord(address, write.value, 5); err != nil {
			return StepResult{}, err
		}
		transactions = append(transactions, writeTransaction(address, 5, write.value))
	}

	vectorHigh, err := c.Bus.ReadWord(vectorAddress, 5)
	if err != nil {
		return StepResult{}, err
	}
	vectorLow, err := c.Bus.ReadWord(vectorAddress+2, 5)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions,
		readTransaction(vectorAddress, 5, vectorHigh),
		readTransaction(vectorAddress+2, 5, vectorLow))
	handler := uint32(vectorHigh)<<16 | uint32(vectorLow)
	handlerAddress := handler & addressMask
	first, err := c.Bus.ReadWord(handlerAddress, 6)
	if err != nil {
		return StepResult{}, err
	}
	secondAddress := (handler + 2) & addressMask
	second, err := c.Bus.ReadWord(secondAddress, 6)
	if err != nil {
		return StepResult{}, err
	}
	transactions = append(transactions,
		readTransaction(handlerAddress, 6, first),
		readTransaction(secondAddress, 6, second))

	c.State.SSP = newSP
	c.State.SR = originalSR | supervisor
	c.State.SR &^= 0x8000
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = handler + 4
	return StepResult{Clocks: clocks, Transactions: transactions}, nil
}

func (c *CPU) refillBranch(address uint32, clocks uint32) (StepResult, error) {
	fc := c.programFunctionCode()
	firstAddress := address & addressMask
	first, err := c.Bus.ReadWord(firstAddress, fc)
	if err != nil {
		return StepResult{}, err
	}
	secondAddress := (address + 2) & addressMask
	second, err := c.Bus.ReadWord(secondAddress, fc)
	if err != nil {
		return StepResult{}, err
	}
	c.State.Prefetch = [2]uint16{first, second}
	c.State.PC = address + 4
	return StepResult{Clocks: clocks, Transactions: []Transaction{
		readTransaction(firstAddress, fc, first),
		readTransaction(secondAddress, fc, second),
	}}, nil
}

func branchCondition(condition uint8, sr uint16) bool {
	c := sr&0x0001 != 0
	v := sr&0x0002 != 0
	z := sr&0x0004 != 0
	n := sr&0x0008 != 0
	switch condition {
	case 2:
		return !c && !z
	case 3:
		return c || z
	case 4:
		return !c
	case 5:
		return c
	case 6:
		return !z
	case 7:
		return z
	case 8:
		return !v
	case 9:
		return v
	case 10:
		return !n
	case 11:
		return n
	case 12:
		return n == v
	case 13:
		return n != v
	case 14:
		return !z && n == v
	case 15:
		return z || n != v
	default:
		return false
	}
}

func (c *CPU) programFunctionCode() uint8 {
	if c.State.SR&supervisor != 0 {
		return 6
	}
	return 2
}

func readTransaction(address uint32, fc uint8, data uint16) Transaction {
	return Transaction{Kind: "r", Cycle: 4, FC: fc, Address: address, Size: 2,
		Data: data, UDS: true, LDS: true}
}

func writeTransaction(address uint32, fc uint8, data uint16) Transaction {
	return Transaction{Kind: "w", Cycle: 4, FC: fc, Address: address, Size: 2,
		Data: data, UDS: true, LDS: true}
}

func readByteTransaction(address uint32, fc uint8, data byte) Transaction {
	return byteTransaction("r", address, fc, data)
}

func writeByteTransaction(address uint32, fc uint8, data byte) Transaction {
	return byteTransaction("w", address, fc, data)
}

func byteTransaction(kind string, address uint32, fc uint8, data byte) Transaction {
	transaction := Transaction{Kind: kind, Cycle: 4, FC: fc, Address: address &^ 1, Size: 1}
	if address&1 == 0 {
		transaction.Data = uint16(data) << 8
		transaction.UDS = true
	} else {
		transaction.Data = uint16(data)
		transaction.LDS = true
	}
	return transaction
}

func (c *CPU) setLogicalFlags(value uint32, bits uint8) {
	c.State.SR &^= 0x000f
	mask := uint32(0xffff_ffff)
	negative := uint32(0x8000_0000)
	if bits == 8 {
		mask = 0x0000_00ff
		negative = 0x0000_0080
	} else if bits == 16 {
		mask = 0x0000_ffff
		negative = 0x0000_8000
	}
	value &= mask
	if value == 0 {
		c.State.SR |= 0x0004
	}
	if value&negative != 0 {
		c.State.SR |= 0x0008
	}
}
