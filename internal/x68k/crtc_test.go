package x68k

import "testing"

// $E80481 的動作位元兩個方向都會被等：設起來要讀得到 1，然後要自己變回 0。
// 這一支就是那兩個迴圈的縮影——任一方向卡住，SANMAIN.Z 就開不了機。
func TestCRTCOpBitSetsThenClears(t *testing.T) {
	c := NewCRTC()
	c.Write(1000, 0x02)
	if got := c.Read(1000); got&0x02 == 0 {
		t.Fatalf("寫進去之後馬上讀是 0x%02X，第一個迴圈會卡死", got)
	}
	if got := c.Read(1000 + c.FrameCycles - 1); got&0x02 == 0 {
		t.Errorf("還沒到一格畫面就清掉了：0x%02X", got)
	}
	if got := c.Read(1000 + c.FrameCycles); got&0x02 != 0 {
		t.Fatalf("過了一格畫面還沒清掉是 0x%02X，第二個迴圈會卡死", got)
	}
}

// 沒有動作位元的寫入不該啟動計時，也不該被清掉。
func TestCRTCOpKeepsOtherBits(t *testing.T) {
	c := NewCRTC()
	c.Write(0, 0x01)
	if got := c.Read(c.FrameCycles * 10); got != 0x01 {
		t.Fatalf("非動作位元被動到了：0x%02X", got)
	}
}
