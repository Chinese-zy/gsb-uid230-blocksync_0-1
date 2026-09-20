package blocksync

// BlockSig 描述源数据里一个定长块的核对信息。
type BlockSig struct {
	Index  int
	Len    int
	Weak   uint32
	Strong uint32
}

// Signature 把 data 按 blockSize 切块，逐块算出弱、硬核对。
func Signature(data []byte, blockSize int) []BlockSig {
	if blockSize <= 0 {
		panic("blocksync: blockSize 必须为正")
	}
	var sigs []BlockSig
	for off, idx := 0, 0; off < len(data); off, idx = off+blockSize, idx+1 {
		end := off + blockSize
		if end > len(data) {
			end = len(data)
		}
		blk := data[off:end]
		sigs = append(sigs, BlockSig{
			Index:  idx,
			Len:    len(blk),
			Weak:   weakSum(blk).sum(),
			Strong: strongSum(blk),
		})
	}
	return sigs
}
