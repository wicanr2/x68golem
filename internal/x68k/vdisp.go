package x68k

import (
	"encoding/binary"
	"fmt"

	"github.com/wicanr2/x68golem/m68k"
)

// 垂直同步中斷：`$6C _VDISPST`。
//
// 這是主迴圈的入口。遊戲把一支處理常式登記進來，之後每隔幾格畫面被叫一次；
// 動畫、計時、按鍵輪詢都掛在那上面。
//
// ## 處理常式以 `rte` 結尾——這是量出來的，不是查來的
//
// 第一版假設它以 `rts` 結尾（IOCS 用 `jsr` 呼叫它），於是只推了 4 bytes 的
// 回返位址。結果程式跑進一片 0，而 **SSP 停在基準值 +2**：推 4、彈 6，
// 差 2 bytes——那正是 `rte` 的形狀（彈一個 word 的 SR 加一個 long 的 PC）。
//
// 所以這裡推的是**完整的例外堆疊框**（SR word ＋ PC long），PC 指向回返樁。
// 處理常式 `rte` 之後就落在樁上，我們再把整個 CPU 狀態還原回去。
//
// 「整個狀態都還原」對應的是 IOCS 的 `movem` 存還原：處理常式之間只能
// 透過記憶體溝通，不能靠暫存器。
const retStub = 0x1F0020

// InstallVDisp 登記 `_VDISPST`。
func (m *Machine) InstallVDisp() {
	m.IOCSCalls[0x6C] = iocsVdispst
}

// iocsVdispst 是 `$6C _VDISPST`（d1 的高 byte ＝ 期間、低 byte ＝ 次數、
// a1.l ＝ 處理常式位址，0 表示取消）。回傳前一支處理常式的位址。
func iocsVdispst(m *Machine) error {
	count := uint32(m.CPU.State.D[1] & 0xFF)
	if count == 0 {
		count = 256
	}
	old := m.vdispHandler
	m.vdispHandler = m.CPU.State.A[1]
	m.vdispCount = count
	m.vdispPeriod = byte(m.CPU.State.D[1] >> 8)
	if m.vdispHandler != 0 {
		m.vdispNextAt = m.cycles + m.frameCycles()*uint64(count)
	}
	m.SetResult(old)
	return nil
}

func (m *Machine) frameCycles() uint64 {
	if m.Bus.CRTC != nil && m.Bus.CRTC.FrameCycles > 0 {
		return m.Bus.CRTC.FrameCycles
	}
	return 10_000_000 / 55
}

// serviceVDisp 在該叫的時候叫垂直同步的處理常式。
// 回傳是否已經跳過去了（跳過去的話這一步不要再執行原本的指令）。
func (m *Machine) serviceVDisp() (bool, error) {
	if m.vdispHandler == 0 || m.cycles < m.vdispNextAt || len(m.callStack) > 0 {
		return false, nil
	}
	m.vdispNextAt = m.cycles + m.frameCycles()*uint64(m.vdispCount)
	m.VDispCalls++
	if err := m.callInterrupt(m.vdispHandler); err != nil {
		return false, err
	}
	return true, nil
}

// callInterrupt 讓機器去跑一支中斷處理常式，做完再回到原來的地方。
//
// 推的是 68000 的 group 1／2 例外堆疊框：SSP+0 是 SR，SSP+2 是回返 PC。
// 處理常式的 `rte` 會把兩者彈回來，PC 落在回返樁上。
func (m *Machine) callInterrupt(addr uint32) error {
	m.callStack = append(m.callStack, m.CPU.State)
	sr := m.CPU.State.SR | 0x2000 // 中斷發生時是 supervisor
	sp := m.CPU.State.SSP - 6
	if int(sp)+5 >= len(m.Bus.RAM) {
		return fmt.Errorf("x68k: supervisor 堆疊 0x%X 不在主記憶體裡", sp)
	}
	binary.BigEndian.PutUint16(m.Bus.RAM[sp:], sr)
	binary.BigEndian.PutUint32(m.Bus.RAM[sp+2:], retStub)
	m.CPU.State.SR = sr
	m.CPU.State.SSP = sp
	return m.resume(addr)
}

// returnFromSub 在處理常式回到樁位址時把狀態還原。
func (m *Machine) returnFromSub() error {
	if m.ServiceLog != nil {
		m.ServiceLog("  ↳從中斷處理常式返回（整個狀態還原）")
	}
	if len(m.callStack) == 0 {
		return fmt.Errorf("x68k: 回到回返樁但沒有存下來的狀態")
	}
	var st m68k.State
	st, m.callStack = m.callStack[len(m.callStack)-1], m.callStack[:len(m.callStack)-1]
	m.CPU.State = st
	return nil
}
