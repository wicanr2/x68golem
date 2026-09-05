package x68k

// IOCS 的 peek／poke 家族（`$82`–`$89`）。
//
// 這一組存在的理由是 supervisor 存取：user mode 的程式用它們去讀寫
// 一般讀不到的位址。所有的位址都在 A1（`_B_MEMSTR`／`_B_MEMSET` 另用 A2），
// 而且**A1 會自己往前走**。
//
// 來源：Data Crystal 的 IOCS 手冊整理（平台公開規格）。
//
// `FLOAT2.X` 一裝好就用 `$84 _B_LPEEK` 去看向量表——所以要跑那支驅動，
// 這一組是必要的（`docs/findings/014`）。
func (m *Machine) InstallPeekPoke() {
	m.IOCSCalls[0x82] = func(mm *Machine) error { return peek(mm, 1) }
	m.IOCSCalls[0x83] = func(mm *Machine) error { return peek(mm, 2) }
	m.IOCSCalls[0x84] = func(mm *Machine) error { return peek(mm, 4) }
	m.IOCSCalls[0x85] = iocsMemstr
	m.IOCSCalls[0x86] = func(mm *Machine) error { return poke(mm, 1) }
	m.IOCSCalls[0x87] = func(mm *Machine) error { return poke(mm, 2) }
	m.IOCSCalls[0x88] = func(mm *Machine) error { return poke(mm, 4) }
	m.IOCSCalls[0x89] = iocsMemset
}

func peek(m *Machine, size uint32) error {
	a := m.CPU.State.A[1]
	var v uint32
	switch size {
	case 1:
		b, err := m.Bus.ReadByte(a, 5)
		if err != nil {
			return err
		}
		v = uint32(b)
	case 2:
		w, err := m.Bus.ReadWord(a, 5)
		if err != nil {
			return err
		}
		v = uint32(w)
	default:
		l, err := m.readLong(a)
		if err != nil {
			return err
		}
		v = l
	}
	m.CPU.State.A[1] = a + size
	m.SetResult(v)
	return nil
}

func poke(m *Machine, size uint32) error {
	a := m.CPU.State.A[1]
	v := m.CPU.State.D[0]
	var err error
	switch size {
	case 1:
		err = m.Bus.WriteByte(a, byte(v), 5)
	case 2:
		err = m.Bus.WriteWord(a, uint16(v), 5)
	default:
		err = m.writeLong(a, v)
	}
	if err != nil {
		return err
	}
	m.CPU.State.A[1] = a + size
	m.SetResult(m.CPU.State.A[1])
	return nil
}

// iocsMemstr 是 `$85 _B_MEMSTR`：從 (a1)++ 搬 d1 個 byte 到 (a2)++。
func iocsMemstr(m *Machine) error { return memcopy(m, 1, 2) }

// iocsMemset 是 `$89 _B_MEMSET`：從 (a2)++ 搬 d1 個 byte 到 (a1)++。
func iocsMemset(m *Machine) error { return memcopy(m, 2, 1) }

func memcopy(m *Machine, src, dst int) error {
	n := m.CPU.State.D[1]
	s, d := m.CPU.State.A[src], m.CPU.State.A[dst]
	for i := uint32(0); i < n; i++ {
		b, err := m.Bus.ReadByte(s+i, 5)
		if err != nil {
			return err
		}
		if err := m.Bus.WriteByte(d+i, b, 5); err != nil {
			return err
		}
	}
	m.CPU.State.A[src] = s + n
	m.CPU.State.A[dst] = d + n
	m.SetResult(m.CPU.State.A[1])
	return nil
}
