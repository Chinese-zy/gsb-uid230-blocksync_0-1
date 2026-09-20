package blocksync

// Op 是同步指令：要么引用源数据的整块，要么携带字面字节。
type Op struct {
	Copy    bool // true 表示引用源数据的第 Index 块
	Index   int
	Literal []byte
}

// Delta 用滚动弱核对在 data 上滑窗，弱核对撞上后再用硬核对确认；
// 硬核对不一致的两块绝不合并，窗口继续逐字节滚动。
func Delta(sigs []BlockSig, data []byte, blockSize int) []Op {
	byWeak := make(map[uint32][]BlockSig)
	for _, s := range sigs {
		byWeak[s.Weak] = append(byWeak[s.Weak], s)
	}

	var ops []Op
	var lit []byte
	flush := func() {
		if len(lit) > 0 {
			ops = append(ops, Op{Literal: append([]byte(nil), lit...)})
			lit = lit[:0]
		}
	}

	n := len(data)
	i := 0
	var w weakChecksum
	have := -1 // 当前窗口起点，have != i 表示窗口无效
	for i < n {
		win := n - i
		if win > blockSize {
			win = blockSize
		}
		matched := false
		if win == blockSize {
			if have != i {
				w = weakSum(data[i : i+blockSize])
				have = i
			}
			for _, s := range byWeak[w.sum()] {
				if s.Len == blockSize && s.Strong == strongSum(data[i:i+blockSize]) {
					flush()
					ops = append(ops, Op{Copy: true, Index: s.Index})
					i += blockSize
					matched = true
					break
				}
			}
		}
		if !matched {
			lit = append(lit, data[i])
			if i+blockSize < n {
				w.roll(data[i], data[i+blockSize], blockSize)
				have = i + 1
			}
			i++
		}
	}
	flush()
	return ops
}
