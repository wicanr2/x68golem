package x68k

import "fmt"

// callStub 是外部呼叫用的返回位址。函式 `rts` 回到這裡，我們就知道它結束了。
// 與 dosStub／iocsStub／retStub 同一區，都放在程式碰不到的高位。
const callStub = 0x1F0030

// DefaultCallSteps 是 CallSubroutine 的預設上限。
// 給得夠大讓一支公式函式跑完，又小到卡住時很快就會失敗。
const DefaultCallSteps = 5_000_000

// CallSubroutine 從外面呼叫程式裡的一支函式，參數照 C 慣例推到堆疊上。
//
// **這是無頭執行器與模擬器的分水嶺**：MAME 只能看，這裡可以問
// 「同一組輸入餵給原版的這支函式，它回什麼」。對拍公式時要的正是這個——
// 不必把遊戲玩到那個畫面，也不必猜函式在做什麼。
//
// 慣例與 `runtime/xc` 同一份（參數由右往左推、`4(sp)` 是第一個參數、
// D0 回傳、呼叫端收拾堆疊）。副作用**不會**還原：函式改了記憶體就是改了，
// 要乾淨的起點請自己 `Snapshot`／`Restore`——那是呼叫端的決定，不是這裡的。
//
// 呼叫期間服務攔截、hook、intercept 全部照常運作，所以可以先把不想跑的
// 子函式（繪圖、音效）用 InstallIntercept 換掉，只留下要對照的那一段。
func (m *Machine) CallSubroutine(addr uint32, args ...uint32) (uint32, error) {
	return m.CallSubroutineN(DefaultCallSteps, addr, args...)
}

// CallSubroutineN 同 CallSubroutine，另外指定指令數上限。
func (m *Machine) CallSubroutineN(maxSteps int, addr uint32, args ...uint32) (uint32, error) {
	sp := &m.CPU.State.USP
	if m.CPU.State.SR&0x2000 != 0 {
		sp = &m.CPU.State.SSP
	}
	entry := *sp
	for i := len(args) - 1; i >= 0; i-- {
		*sp -= 4
		if err := m.writeLong(*sp, args[i]); err != nil {
			return 0, err
		}
	}
	*sp -= 4
	if err := m.writeLong(*sp, callStub); err != nil {
		return 0, err
	}
	if err := m.resume(addr); err != nil {
		return 0, err
	}
	for i := 0; i < maxSteps; i++ {
		if m.CPU.State.PC-4 == callStub {
			// **先檢查堆疊平不平**：`rts` 之後剩下的應該剛好是我們推的參數。
			// 對不上就是呼叫慣例給錯了（參數個數、或那支函式自己收拾堆疊），
			// 而那種錯會安靜地把之後的執行全部帶歪——所以在這裡就停。
			want := entry - uint32(len(args))*4
			if *sp != want {
				return 0, fmt.Errorf(
					"x68k: 呼叫 0x%06X 回來時堆疊是 0x%06X，應該是 0x%06X"+
						"（差 %d bytes——參數個數或呼叫慣例不對）",
					addr, *sp, want, int32(*sp)-int32(want))
			}
			// 呼叫端收拾堆疊：參數是我們推的，回位址已經被 rts 取走。
			*sp = entry
			return m.CPU.State.D[0], nil
		}
		if err := m.Step(); err != nil {
			return 0, fmt.Errorf("x68k: 呼叫 0x%06X 第 %d 步：%w", addr, i, err)
		}
	}
	return 0, fmt.Errorf("x68k: 呼叫 0x%06X 跑滿 %d 道指令還沒回來（PC=0x%06X）",
		addr, maxSteps, m.CPU.State.PC-4)
}
