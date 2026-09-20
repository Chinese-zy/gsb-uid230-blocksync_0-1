package blocksync

// Reconstruct 按 ops 从源数据取块、拼字面字节，得到目标数据。
func Reconstruct(sigs []BlockSig, src []byte, ops []Op, blockSize int) []byte {
	var out []byte
	for _, op := range ops {
		if op.Copy {
			off := op.Index * blockSize
			end := off + blockSize
			if end > len(src) {
				end = len(src)
			}
			out = append(out, src[off:end]...)
		} else {
			out = append(out, op.Literal...)
		}
	}
	return out
}

// SyncTo 把 ops 逐块落成定长块写入 Store，全部提交后目标数据即完整可吐。
func SyncTo(store *Store, sigs []BlockSig, src []byte, ops []Op, blockSize int) error {
	data := Reconstruct(sigs, src, ops, blockSize)
	for off := 0; off < len(data); off += blockSize {
		end := off + blockSize
		if end > len(data) {
			end = len(data)
		}
		if err := store.Append(data[off:end]); err != nil {
			return err
		}
	}
	return nil
}
