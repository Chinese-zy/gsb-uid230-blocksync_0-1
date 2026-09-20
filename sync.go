package blocksync

import "errors"

// ErrUnalignedStream 表示源数据长度不是固定块长的整数倍；本实现只核对整固定块。
var ErrUnalignedStream = errors.New("blocksync: stream length is not a multiple of blockSize")

// PeerSums 是对端已经持有块的核对表。
// 同一弱值可以挂多块（弱碰撞），所以强值是 [][32]byte 而不是单值。
type PeerSums struct {
	BlockSize int
	Weak      []uint32
	Strong    [][32]byte
}

// SyncResult 是一次同步的统计。
type SyncResult struct {
	Blocks     int // 源端总块数
	Reused     int // 弱+强两级核对一致、直接复用的块数
	Sent       int // 对端没有、真正发送的块数
	Collisions int // 弱值相同但强值不同（弱核对撞上）的次数
}

// BlockSender 接收“对端缺失、按原顺序切开重传”的新块。
// index 为该块在源端的原始块序号，保证严格递增。
type BlockSender func(index int, block []byte) error

// RunSync 对一份长度为块长整数倍的数据做固定块长滚动核对同步。
//
// 流程（每一块独立滚动，块边界 Reset，不跨块）：
//  1. 弱核对命中：在对端同弱值的候选块里逐个比强值；
//  2. 强值也相等：两级核对一致，复用对端块，不重发；
//  3. 候选全比完仍无强值相等：说明弱核对撞上了——两块不是同一块，
//     必须切开，把本块作为新块通过 sender 按序发出。
func RunSync(data []byte, blockSize int, modulus uint32, peer PeerSums, sender BlockSender) (SyncResult, error) {
	if blockSize <= 0 {
		return SyncResult{}, errors.New("blocksync: blockSize must be positive")
	}
	if len(data)%blockSize != 0 {
		return SyncResult{}, ErrUnalignedStream
	}
	if peer.BlockSize != 0 && peer.BlockSize != blockSize {
		return SyncResult{}, errors.New("blocksync: peer block size mismatch")
	}

	byWeak := make(map[uint32][]int, len(peer.Weak))
	for i, w := range peer.Weak {
		byWeak[w] = append(byWeak[w], i)
	}

	h := NewRollingHasher(modulus)
	res := SyncResult{Blocks: len(data) / blockSize}
	for idx := 0; idx < res.Blocks; idx++ {
		block := data[idx*blockSize : (idx+1)*blockSize]

		h.Reset()
		h.Write(block)
		w := h.Sum32()

		same := false
		collided := false
		for _, cand := range byWeak[w] {
			if cand >= len(peer.Strong) {
				continue
			}
			if peer.Strong[cand] == StrongSum(block) {
				same = true
				break
			}
			collided = true
		}

		if same {
			res.Reused++
			continue
		}

		// 弱值在对端出现过、却没有任何一块强值相同：这就是弱核对撞，
		// 不能把两块当同一块，必须切开重传。
		if collided {
			res.Collisions++
		}
		res.Sent++
		if sender != nil {
			if err := sender(idx, block); err != nil {
				return res, err
			}
		}
	}

	return res, nil
}

// PeerSumsFromStore 从已落盘的块存储构造对端核对表。
func PeerSumsFromStore(st *Store, modulus uint32) (PeerSums, error) {
	blocks, err := st.Blocks()
	if err != nil {
		return PeerSums{}, err
	}
	sums := PeerSums{
		BlockSize: st.blockSize,
		Weak:      make([]uint32, 0, len(blocks)),
		Strong:    make([][32]byte, 0, len(blocks)),
	}
	for _, b := range blocks {
		sums.Weak = append(sums.Weak, WeakSum(modulus, b))
		sums.Strong = append(sums.Strong, StrongSum(b))
	}
	return sums, nil
}
