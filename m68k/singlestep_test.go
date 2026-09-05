package m68k

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type corpusState struct {
	CPU State
	RAM SparseMemory
}

type corpusTest struct {
	Name         string
	Initial      corpusState
	Final        corpusState
	Transactions []Transaction
	Timeline     []BusPhase
	Clocks       uint32
}

func TestSingleStepNOP(t *testing.T) {
	testSingleStepCorpus(t, "NOP.json.bin")
}

func TestSingleStepLineF(t *testing.T) {
	testSingleStepCorpus(t, "ILLEGAL_LINEF.json.bin")
}

func TestSingleStepMOVEQ(t *testing.T) {
	testSingleStepCorpus(t, "MOVE.q.json.bin")
}

func TestSingleStepSWAP(t *testing.T) {
	testSingleStepCorpus(t, "SWAP.json.bin")
}

func TestSingleStepEXTW(t *testing.T) {
	testSingleStepCorpus(t, "EXT.w.json.bin")
}

func TestSingleStepEXTL(t *testing.T) {
	testSingleStepCorpus(t, "EXT.l.json.bin")
}

func TestSingleStepCLRByte(t *testing.T) {
	testSingleStepCorpus(t, "CLR.b.json.bin")
}

func TestSingleStepCLRWord(t *testing.T) {
	testSingleStepCorpus(t, "CLR.w.json.bin")
}

func TestSingleStepCLRLong(t *testing.T) {
	testSingleStepCorpus(t, "CLR.l.json.bin")
}

func TestSingleStepMOVEMWord(t *testing.T) {
	testSingleStepCorpus(t, "MOVEM.w.json.bin")
}

func TestSingleStepMOVEMLong(t *testing.T) {
	testSingleStepCorpus(t, "MOVEM.l.json.bin")
}

func TestSingleStepLINK(t *testing.T) {
	testSingleStepCorpus(t, "LINK.json.bin")
}

func TestSingleStepTSTByte(t *testing.T) {
	testSingleStepCorpus(t, "TST.b.json.bin")
}

func TestSingleStepTSTWord(t *testing.T) {
	testSingleStepCorpus(t, "TST.w.json.bin")
}

func TestSingleStepTSTLong(t *testing.T) {
	testSingleStepCorpus(t, "TST.l.json.bin")
}

func TestSingleStepBcc(t *testing.T) {
	testSingleStepCorpus(t, "Bcc.json.bin")
}

func TestSingleStepBSR(t *testing.T) {
	testSingleStepCorpus(t, "BSR.json.bin")
}

func TestSingleStepRTS(t *testing.T) {
	testSingleStepCorpus(t, "RTS.json.bin")
}

func TestSingleStepTRAP(t *testing.T) {
	testSingleStepCorpus(t, "TRAP.json.bin")
}

func TestSingleStepRTE(t *testing.T) {
	testSingleStepCorpus(t, "RTE.json.bin")
}

func TestSingleStepJMP(t *testing.T) {
	testSingleStepCorpus(t, "JMP.json.bin")
}

func TestSingleStepJSR(t *testing.T) {
	testSingleStepCorpus(t, "JSR.json.bin")
}

func TestSingleStepLEA(t *testing.T) {
	testSingleStepCorpus(t, "LEA.json.bin")
}

func TestSingleStepPEA(t *testing.T) {
	testSingleStepCorpus(t, "PEA.json.bin")
}

func TestSingleStepMOVEByteSourcesToDn(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.b.json.bin", 384, func(test corpusTest) bool {
		return test.Initial.CPU.Prefetch[0]>>6&7 == 0
	})
}

func TestSingleStepMOVEByteMemoryDestinations(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.b.json.bin", 2116, func(test corpusTest) bool {
		return test.Initial.CPU.Prefetch[0]>>6&7 != 0
	})
}

func TestSingleStepMOVEWordNormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.w.json.bin", 1013, func(test corpusTest) bool {
		return !hasTransactionKind(test.Transactions, "re") && !hasTransactionKind(test.Transactions, "we")
	})
}

func TestSingleStepMOVEWordReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.w.json.bin", 839, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepMOVEWordWriteAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.w.json.bin", 648, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "we")
	})
}

func TestSingleStepMOVELongNormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.l.json.bin", 1013, func(test corpusTest) bool {
		return !hasTransactionKind(test.Transactions, "re") && !hasTransactionKind(test.Transactions, "we")
	})
}

func TestSingleStepMOVELongReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.l.json.bin", 869, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepMOVELongWriteAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVE.l.json.bin", 618, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "we")
	})
}

func TestSingleStepMOVEAWordNormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVEA.w.json.bin", 1658, func(test corpusTest) bool {
		return !hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepMOVEAWordReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVEA.w.json.bin", 842, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepMOVEALongNormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVEA.l.json.bin", 1655, func(test corpusTest) bool {
		return !hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepMOVEALongReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "MOVEA.l.json.bin", 845, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepADDAWordNormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "ADDA.w.json.bin", 1683, func(test corpusTest) bool {
		return !hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepADDAWordReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "ADDA.w.json.bin", 817, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepADDALongNormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "ADDA.l.json.bin", 1675, func(test corpusTest) bool {
		return !hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepADDALongReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "ADDA.l.json.bin", 825, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepANDByteEAToDn(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.b.json.bin", 1317, andEAToDn)
}

func TestSingleStepANDByteDnToEA(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.b.json.bin", 1007, func(test corpusTest) bool {
		opcode := test.Initial.CPU.Prefetch[0]
		return opcode&0xf000 == 0xc000 && opcode>>6&7 == 4
	})
}

func TestSingleStepANDImmediateByte(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.b.json.bin", 176, func(test corpusTest) bool {
		return test.Initial.CPU.Prefetch[0]&0xff00 == 0x0200
	})
}

func TestSingleStepANDImmediateWord(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.w.json.bin", 158, func(test corpusTest) bool {
		return test.Initial.CPU.Prefetch[0]&0xff00 == 0x0200
	})
}

func TestSingleStepANDImmediateLong(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.l.json.bin", 129, func(test corpusTest) bool {
		return test.Initial.CPU.Prefetch[0]&0xff00 == 0x0200
	})
}

func TestSingleStepADDByte(t *testing.T) {
	testSingleStepCorpus(t, "ADD.b.json.bin")
}

func TestSingleStepADDWord(t *testing.T) {
	testSingleStepCorpus(t, "ADD.w.json.bin")
}

func TestSingleStepADDLong(t *testing.T) {
	testSingleStepCorpus(t, "ADD.l.json.bin")
}

func TestSingleStepSUB(t *testing.T) {
	for _, item := range []struct {
		name   string
		file   string
		want   int
		filter func(corpusTest) bool
	}{
		{"byte-sub", "SUB.b.json.bin", 1567, isSUB},
		{"byte-subi", "SUB.b.json.bin", 122, isSUBImmediate},
		{"byte-subq", "SUB.b.json.bin", 811, isSUBQuick},
		{"word-sub", "SUB.w.json.bin", 1514, isSUB},
		{"word-subi", "SUB.w.json.bin", 100, isSUBImmediate},
		{"word-subq", "SUB.w.json.bin", 886, isSUBQuick},
		{"long-sub", "SUB.l.json.bin", 1527, isSUB},
		{"long-subi", "SUB.l.json.bin", 115, isSUBImmediate},
		{"long-subq", "SUB.l.json.bin", 858, isSUBQuick},
	} {
		t.Run(item.name, func(t *testing.T) { testSingleStepCorpusFiltered(t, item.file, item.want, item.filter) })
	}
}

func TestSingleStepASLByte(t *testing.T) {
	testSingleStepCorpus(t, "ASL.b.json.bin")
}

func TestSingleStepASLWord(t *testing.T) {
	testSingleStepCorpus(t, "ASL.w.json.bin")
}

func TestSingleStepASLLong(t *testing.T) {
	testSingleStepCorpus(t, "ASL.l.json.bin")
}

func TestSingleStepASRByte(t *testing.T) {
	testSingleStepCorpus(t, "ASR.b.json.bin")
}

func TestSingleStepASRWord(t *testing.T) {
	testSingleStepCorpus(t, "ASR.w.json.bin")
}

func TestSingleStepASRLong(t *testing.T) {
	testSingleStepCorpus(t, "ASR.l.json.bin")
}

func TestSingleStepLSRByte(t *testing.T) {
	testSingleStepCorpus(t, "LSR.b.json.bin")
}

func TestSingleStepLSRWord(t *testing.T) {
	testSingleStepCorpus(t, "LSR.w.json.bin")
}

func TestSingleStepLSRLong(t *testing.T) {
	testSingleStepCorpus(t, "LSR.l.json.bin")
}

func TestSingleStepLSLByte(t *testing.T) {
	testSingleStepCorpus(t, "LSL.b.json.bin")
}

func TestSingleStepLSLWord(t *testing.T) {
	testSingleStepCorpus(t, "LSL.w.json.bin")
}

func TestSingleStepLSLLong(t *testing.T) {
	testSingleStepCorpus(t, "LSL.l.json.bin")
}

func TestSingleStepRORByte(t *testing.T) {
	testSingleStepCorpus(t, "ROR.b.json.bin")
}

func TestSingleStepRORWord(t *testing.T) {
	testSingleStepCorpus(t, "ROR.w.json.bin")
}

func TestSingleStepRORLong(t *testing.T) {
	testSingleStepCorpus(t, "ROR.l.json.bin")
}

func TestSingleStepROLByte(t *testing.T) {
	testSingleStepCorpus(t, "ROL.b.json.bin")
}

func TestSingleStepROLWord(t *testing.T) {
	testSingleStepCorpus(t, "ROL.w.json.bin")
}

func TestSingleStepROLLong(t *testing.T) {
	testSingleStepCorpus(t, "ROL.l.json.bin")
}

func TestSingleStepSUBA(t *testing.T) {
	for _, name := range []string{"SUBA.w.json.bin", "SUBA.l.json.bin"} {
		t.Run(name, func(t *testing.T) { testSingleStepCorpus(t, name) })
	}
}

func TestSingleStepCMPA(t *testing.T) {
	for _, name := range []string{"CMPA.w.json.bin", "CMPA.l.json.bin"} {
		t.Run(name, func(t *testing.T) { testSingleStepCorpus(t, name) })
	}
}

func TestSingleStepEXG(t *testing.T) {
	testSingleStepCorpus(t, "EXG.json.bin")
}

func TestSingleStepMOVEUSP(t *testing.T) {
	for _, name := range []string{"MOVEtoUSP.json.bin", "MOVEfromUSP.json.bin"} {
		t.Run(name, func(t *testing.T) { testSingleStepCorpus(t, name) })
	}
}

func TestSingleStepMOVEToStatus(t *testing.T) {
	for _, name := range []string{"MOVEtoCCR.json.bin", "MOVEtoSR.json.bin"} {
		t.Run(name, func(t *testing.T) { testSingleStepCorpus(t, name) })
	}
}

func TestSingleStepMOVEFromSR(t *testing.T) {
	testSingleStepCorpus(t, "MOVEfromSR.json.bin")
}

func TestSingleStepTAS(t *testing.T) {
	testSingleStepCorpusAdjusted(t, "TAS.json.bin", 2500, func(corpusTest) bool { return true }, func(test *corpusTest) {
		if test.Initial.CPU.Prefetch[0]>>3&7 == 0 {
			return
		}
		// 上游明載其 TAS 5-cycle RMW timing 不正確；兩個 Hatari／Atari ST
		// 案例都確認 (An) 為 16 clocks，因此只在此語料入口套用局部勘誤。
		test.Clocks += 2
	})
}

func TestSingleStepMULS(t *testing.T) {
	testSingleStepCorpus(t, "MULS.json.bin")
}

func TestSingleStepMULU(t *testing.T) {
	testSingleStepCorpus(t, "MULU.json.bin")
}

func TestSingleStepDIVS(t *testing.T) {
	testSingleStepCorpus(t, "DIVS.json.bin")
}

func TestSingleStepDIVU(t *testing.T) {
	testSingleStepCorpus(t, "DIVU.json.bin")
}

func TestSingleStepNOTByte(t *testing.T) {
	testSingleStepCorpus(t, "NOT.b.json.bin")
}

func TestSingleStepNOTWord(t *testing.T) {
	testSingleStepCorpus(t, "NOT.w.json.bin")
}

func TestSingleStepNOTLong(t *testing.T) {
	testSingleStepCorpus(t, "NOT.l.json.bin")
}

func TestSingleStepNEGByte(t *testing.T) {
	testSingleStepCorpus(t, "NEG.b.json.bin")
}

func TestSingleStepNEGWord(t *testing.T) {
	testSingleStepCorpus(t, "NEG.w.json.bin")
}

func TestSingleStepNEGLong(t *testing.T) {
	testSingleStepCorpus(t, "NEG.l.json.bin")
}

func TestSingleStepScc(t *testing.T) {
	testSingleStepCorpus(t, "Scc.json.bin")
}

func TestSingleStepDBcc(t *testing.T) {
	testSingleStepCorpus(t, "DBcc.json.bin")
}

func TestSingleStepBitOperations(t *testing.T) {
	for _, name := range []string{"BTST.json.bin", "BCHG.json.bin", "BCLR.json.bin", "BSET.json.bin"} {
		t.Run(name, func(t *testing.T) { testSingleStepCorpus(t, name) })
	}
}

func TestSingleStepUNLINKNormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "UNLINK.json.bin", 1385, func(test corpusTest) bool {
		return !hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepUNLINKAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "UNLINK.json.bin", 1115, func(test corpusTest) bool {
		return hasTransactionKind(test.Transactions, "re")
	})
}

func isSUB(test corpusTest) bool {
	opcode := test.Initial.CPU.Prefetch[0]
	opmode := opcode >> 6 & 7
	return opcode&0xf000 == 0x9000 && (opmode <= 2 || opmode >= 4 && opmode <= 6)
}

func isSUBImmediate(test corpusTest) bool {
	opcode := test.Initial.CPU.Prefetch[0]
	return opcode&0xff00 == 0x0400
}

func isSUBQuick(test corpusTest) bool {
	opcode := test.Initial.CPU.Prefetch[0]
	return opcode&0xf100 == 0x5100
}

func TestSingleStepCMPByte(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.b.json.bin", 1991, isCMP)
}

func TestSingleStepCMPWord(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.w.json.bin", 2032, isCMP)
}

func TestSingleStepCMPLong(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.l.json.bin", 2063, isCMP)
}

func TestSingleStepCMPMemoryByte(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.b.json.bin", 276, isCMPMemory)
}

func TestSingleStepCMPMemoryWord(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.w.json.bin", 261, isCMPMemory)
}

func TestSingleStepCMPMemoryLong(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.l.json.bin", 247, isCMPMemory)
}

func TestSingleStepCMPImmediateByte(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.b.json.bin", 233, isCMPImmediate)
}

func TestSingleStepCMPImmediateWord(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.w.json.bin", 207, isCMPImmediate)
}

func TestSingleStepCMPImmediateLong(t *testing.T) {
	testSingleStepCorpusFiltered(t, "CMP.l.json.bin", 190, isCMPImmediate)
}

func isCMP(test corpusTest) bool {
	opcode := test.Initial.CPU.Prefetch[0]
	return opcode&0xf000 == 0xb000 && opcode>>6&7 <= 2
}

func isCMPMemory(test corpusTest) bool {
	return test.Initial.CPU.Prefetch[0]&0xf138 == 0xb108
}

func isCMPImmediate(test corpusTest) bool {
	return test.Initial.CPU.Prefetch[0]&0xff00 == 0x0c00
}

func TestSingleStepANDWordEAToDn(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.w.json.bin", 1333, andEAToDn)
}

func TestSingleStepANDLongEAToDn(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.l.json.bin", 1315, andEAToDn)
}

func TestSingleStepANDWordDnToEANormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.w.json.bin", 512, func(test corpusTest) bool {
		opcode := test.Initial.CPU.Prefetch[0]
		return opcode&0xf000 == 0xc000 && opcode>>6&7 == 5 && !hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepANDLongDnToEANormal(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.l.json.bin", 597, func(test corpusTest) bool {
		opcode := test.Initial.CPU.Prefetch[0]
		return opcode&0xf000 == 0xc000 && opcode>>6&7 == 6 && !hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepANDWordDnToEAReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.w.json.bin", 497, func(test corpusTest) bool {
		opcode := test.Initial.CPU.Prefetch[0]
		return opcode&0xf000 == 0xc000 && opcode>>6&7 == 5 && hasTransactionKind(test.Transactions, "re")
	})
}

func TestSingleStepANDLongDnToEAReadAddressErrors(t *testing.T) {
	testSingleStepCorpusFiltered(t, "AND.l.json.bin", 459, func(test corpusTest) bool {
		opcode := test.Initial.CPU.Prefetch[0]
		return opcode&0xf000 == 0xc000 && opcode>>6&7 == 6 && hasTransactionKind(test.Transactions, "re")
	})
}

func andEAToDn(test corpusTest) bool {
	opcode := test.Initial.CPU.Prefetch[0]
	return opcode&0xf000 == 0xc000 && opcode>>6&7 <= 2
}

func TestSingleStepOR(t *testing.T) {
	for _, item := range []struct {
		name   string
		file   string
		want   int
		filter func(corpusTest) bool
	}{
		{"byte-ea-dn", "OR.b.json.bin", 1255, orEAToDn},
		{"byte-dn-ea", "OR.b.json.bin", 1084, func(test corpusTest) bool {
			opcode := test.Initial.CPU.Prefetch[0]
			return opcode&0xf000 == 0x8000 && opcode>>6&7 == 4
		}},
		{"byte-imm", "OR.b.json.bin", 161, isORImmediate},
		{"word-ea-dn", "OR.w.json.bin", 1311, orEAToDn},
		{"word-dn-ea", "OR.w.json.bin", 1040, func(test corpusTest) bool {
			opcode := test.Initial.CPU.Prefetch[0]
			return opcode&0xf000 == 0x8000 && opcode>>6&7 == 5
		}},
		{"word-imm", "OR.w.json.bin", 149, isORImmediate},
		{"long-ea-dn", "OR.l.json.bin", 1291, orEAToDn},
		{"long-dn-ea", "OR.l.json.bin", 1049, func(test corpusTest) bool {
			opcode := test.Initial.CPU.Prefetch[0]
			return opcode&0xf000 == 0x8000 && opcode>>6&7 == 6
		}},
		{"long-imm", "OR.l.json.bin", 160, isORImmediate},
	} {
		t.Run(item.name, func(t *testing.T) { testSingleStepCorpusFiltered(t, item.file, item.want, item.filter) })
	}
}

func orEAToDn(test corpusTest) bool {
	opcode := test.Initial.CPU.Prefetch[0]
	return opcode&0xf000 == 0x8000 && opcode>>6&7 <= 2
}

func isORImmediate(test corpusTest) bool {
	opcode := test.Initial.CPU.Prefetch[0]
	return opcode&0xff00 == 0x0000 && opcode&0x003f != 0x003c
}

func TestSingleStepEOR(t *testing.T) {
	for _, name := range []string{"EOR.b.json.bin", "EOR.w.json.bin", "EOR.l.json.bin"} {
		t.Run(name, func(t *testing.T) { testSingleStepCorpus(t, name) })
	}
}

func TestSingleStepSTOP(t *testing.T) {
	testSingleStepCorpus(t, "STOP.json.bin")
}

func hasTransactionKind(transactions []Transaction, kind string) bool {
	for _, transaction := range transactions {
		if transaction.Kind == kind {
			return true
		}
	}
	return false
}

func testSingleStepCorpus(t *testing.T, name string) {
	testSingleStepCorpusFiltered(t, name, 2500, func(corpusTest) bool { return true })
}

func testSingleStepCorpusFiltered(t *testing.T, name string, want int, accept func(corpusTest) bool) {
	testSingleStepCorpusAdjusted(t, name, want, accept, nil)
}

func testSingleStepCorpusAdjusted(t *testing.T, name string, want int, accept func(corpusTest) bool, adjust func(*corpusTest)) {
	t.Helper()
	root := os.Getenv("TALOS_M68000_TESTS")
	if root == "" {
		t.Skip("TALOS_M68000_TESTS is not set; external m68000 corpus not available")
	}
	tests, err := readCorpus(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, test := range tests {
		if !accept(test) {
			continue
		}
		accepted++
		test := test
		if adjust != nil {
			adjust(&test)
		}
		t.Run(test.Name, func(t *testing.T) {
			cpu := CPU{State: test.Initial.CPU, Bus: test.Initial.RAM}
			result, err := cpu.Step()
			if err != nil {
				t.Fatal(err)
			}
			if cpu.State != test.Final.CPU {
				t.Fatalf("state mismatch\n got: %#v\nwant: %#v", cpu.State, test.Final.CPU)
			}
			if !equalSparseMemory(test.Initial.RAM, test.Final.RAM) {
				t.Fatalf("RAM mismatch\n got: %#v\nwant: %#v", test.Initial.RAM, test.Final.RAM)
			}
			if result.Clocks != test.Clocks || !reflect.DeepEqual(result.Transactions, test.Transactions) {
				t.Fatalf("bus mismatch\n got: %#v\nwant: clocks=%d transactions=%#v", result, test.Clocks, test.Transactions)
			}
			if name == "NOP.json.bin" && !reflect.DeepEqual(result.Timeline, test.Timeline) {
				t.Fatalf("timeline mismatch\n got: %#v\nwant: %#v", result.Timeline, test.Timeline)
			}
		})
	}
	if accepted != want {
		t.Fatalf("%s accepted %d tests, want %d", name, accepted, want)
	}
}

func equalSparseMemory(got, want SparseMemory) bool {
	// Corpus byte writes retain the inactive bus lane as an explicit zero;
	// SparseMemory leaves the same unknown zero lane absent.
	for address, value := range got {
		if want[address] != value {
			return false
		}
	}
	for address, value := range want {
		if got[address] != value {
			return false
		}
	}
	return true
}

func readCorpus(path string) ([]corpusTest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	r := bufio.NewReader(file)
	magic, err := readU32(r)
	if err != nil || magic != 0x1a3f5d71 {
		return nil, fmt.Errorf("m68000 corpus header: magic=0x%08x err=%v", magic, err)
	}
	count, err := readU32(r)
	if err != nil {
		return nil, err
	}
	tests := make([]corpusTest, 0, count)
	for i := uint32(0); i < count; i++ {
		test, err := readCorpusTest(r)
		if err != nil {
			return nil, fmt.Errorf("test %d: %w", i, err)
		}
		tests = append(tests, test)
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("m68000 corpus has trailing data")
	}
	return tests, nil
}

func readCorpusTest(r io.Reader) (corpusTest, error) {
	if err := readBlockHeader(r, 0xabc12367); err != nil {
		return corpusTest{}, err
	}
	name, err := readName(r)
	if err != nil {
		return corpusTest{}, err
	}
	initial, err := readCorpusState(r)
	if err != nil {
		return corpusTest{}, err
	}
	final, err := readCorpusState(r)
	if err != nil {
		return corpusTest{}, err
	}
	transactions, timeline, clocks, err := readTransactions(r)
	return corpusTest{Name: name, Initial: initial, Final: final, Transactions: transactions, Timeline: timeline, Clocks: clocks}, err
}

func readName(r io.Reader) (string, error) {
	if err := readBlockHeader(r, 0x89abcdef); err != nil {
		return "", err
	}
	n, err := readU32(r)
	if err != nil || n > 4096 {
		return "", fmt.Errorf("invalid name length %d: %v", n, err)
	}
	name := make([]byte, n)
	_, err = io.ReadFull(r, name)
	return string(name), err
}

func readCorpusState(r io.Reader) (corpusState, error) {
	if err := readBlockHeader(r, 0x01234567); err != nil {
		return corpusState{}, err
	}
	var state State
	for i := range state.D {
		v, err := readU32(r)
		if err != nil {
			return corpusState{}, err
		}
		state.D[i] = v
	}
	for i := range state.A {
		v, err := readU32(r)
		if err != nil {
			return corpusState{}, err
		}
		state.A[i] = v
	}
	var err error
	if state.USP, err = readU32(r); err != nil {
		return corpusState{}, err
	}
	if state.SSP, err = readU32(r); err != nil {
		return corpusState{}, err
	}
	sr, err := readU32(r)
	if err != nil {
		return corpusState{}, err
	}
	state.SR = uint16(sr)
	if state.PC, err = readU32(r); err != nil {
		return corpusState{}, err
	}
	for i := range state.Prefetch {
		v, err := readU32(r)
		if err != nil {
			return corpusState{}, err
		}
		state.Prefetch[i] = uint16(v)
	}
	ramCount, err := readU32(r)
	if err != nil || ramCount > 1<<20 {
		return corpusState{}, fmt.Errorf("invalid RAM count %d: %v", ramCount, err)
	}
	ram := make(SparseMemory, ramCount*2)
	for i := uint32(0); i < ramCount; i++ {
		address, err := readU32(r)
		if err != nil {
			return corpusState{}, err
		}
		var data uint16
		if err := binary.Read(r, binary.LittleEndian, &data); err != nil {
			return corpusState{}, err
		}
		ram[address] = byte(data >> 8)
		ram[address|1] = byte(data)
	}
	return corpusState{CPU: state, RAM: ram}, nil
}

func readTransactions(r io.Reader) ([]Transaction, []BusPhase, uint32, error) {
	if err := readBlockHeader(r, 0x456789ab); err != nil {
		return nil, nil, 0, err
	}
	clocks, err := readU32(r)
	if err != nil {
		return nil, nil, 0, err
	}
	count, err := readU32(r)
	if err != nil || count > 1<<20 {
		return nil, nil, 0, fmt.Errorf("invalid transaction count %d: %v", count, err)
	}
	out := make([]Transaction, 0, count)
	timeline := make([]BusPhase, 0, count)
	var offset uint32
	for i := uint32(0); i < count; i++ {
		var kind uint8
		if err := binary.Read(r, binary.LittleEndian, &kind); err != nil {
			return nil, nil, 0, err
		}
		cycle, err := readU32(r)
		if err != nil {
			return nil, nil, 0, err
		}
		phase := BusPhase{Offset: offset, Cycles: cycle}
		offset += cycle
		if kind == 0 {
			timeline = append(timeline, phase)
			continue
		}
		fc, err := readU32(r)
		if err != nil {
			return nil, nil, 0, err
		}
		address, err := readU32(r)
		if err != nil {
			return nil, nil, 0, err
		}
		data, err := readU32(r)
		if err != nil {
			return nil, nil, 0, err
		}
		uds, err := readU32(r)
		if err != nil {
			return nil, nil, 0, err
		}
		lds, err := readU32(r)
		if err != nil {
			return nil, nil, 0, err
		}
		kinds := map[uint8]string{1: "w", 2: "r", 3: "t", 4: "re", 5: "we"}
		label, ok := kinds[kind]
		if !ok {
			return nil, nil, 0, fmt.Errorf("unknown transaction kind %d", kind)
		}
		size := uint8(1)
		if uds+lds == 2 {
			size = 2
		}
		busData := uint16(data)
		if label == "re" || label == "we" {
			busData = 0
		}
		transaction := Transaction{Kind: label, Cycle: cycle, FC: uint8(fc), Address: address,
			Size: size, Data: busData, UDS: uds != 0, LDS: lds != 0}
		out = append(out, transaction)
		phase.Transaction = &transaction
		timeline = append(timeline, phase)
	}
	if offset != clocks {
		return nil, nil, 0, fmt.Errorf("timeline duration %d does not match instruction clocks %d", offset, clocks)
	}
	return out, timeline, clocks, nil
}

func readBlockHeader(r io.Reader, want uint32) error {
	if _, err := readU32(r); err != nil {
		return err
	}
	magic, err := readU32(r)
	if err != nil {
		return err
	}
	if magic != want {
		return fmt.Errorf("block magic 0x%08x, want 0x%08x", magic, want)
	}
	return nil
}

func readU32(r io.Reader) (uint32, error) {
	var value uint32
	err := binary.Read(r, binary.LittleEndian, &value)
	return value, err
}
