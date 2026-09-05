package m68k

import "fmt"

type SparseMemory map[uint32]byte

func (m SparseMemory) ReadByte(address uint32, _ uint8) (byte, error) {
	address &= addressMask
	value, ok := m[address]
	if !ok {
		return 0, fmt.Errorf("m68k: unmapped byte at 0x%06x", address)
	}
	return value, nil
}

func (m SparseMemory) ReadWord(address uint32, _ uint8) (uint16, error) {
	address &= addressMask
	if address&1 != 0 {
		return 0, fmt.Errorf("m68k: odd word read at 0x%06x", address)
	}
	hi, ok := m[address]
	if !ok {
		return 0, fmt.Errorf("m68k: unmapped byte at 0x%06x", address)
	}
	loAddress := (address + 1) & addressMask
	lo, ok := m[loAddress]
	if !ok {
		return 0, fmt.Errorf("m68k: unmapped byte at 0x%06x", loAddress)
	}
	return uint16(hi)<<8 | uint16(lo), nil
}

func (m SparseMemory) WriteByte(address uint32, value byte, _ uint8) error {
	m[address&addressMask] = value
	return nil
}

func (m SparseMemory) WriteWord(address uint32, value uint16, _ uint8) error {
	address &= addressMask
	if address&1 != 0 {
		return fmt.Errorf("m68k: odd word write at 0x%06x", address)
	}
	m[address] = byte(value >> 8)
	m[(address+1)&addressMask] = byte(value)
	return nil
}
