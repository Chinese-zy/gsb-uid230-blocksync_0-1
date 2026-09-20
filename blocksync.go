// Package blocksync 实现按固定块长滚动核对的块同步。
//
// 核对分两级：
//   - 弱核对：rsync 风格的滚动多项式和，增量计算，用来快速猜测两块“可能相同”；
//     弱核对撞上（两块不同但弱值相等）时不能把它们当成同一块。
//   - 强核对：SHA-256，用来对弱核对命中的候选块做最终确认；强值不等就说明
//     撞上了，必须切开（把当前块当成新块发送）。
package blocksync

import (
	"crypto/sha256"
	"errors"
)

// ErrWeakCollision 在“需要现场暴露弱核对碰撞”的测试/诊断路径上返回。
// 正常同步不会因为碰撞而失败，只会把该块切开重传。
var ErrWeakCollision = errors.New("blocksync: weak checksum collision, blocks differ")

// DefaultChecksumModulus 是弱核对滚动和使用的模数（rsync 取 65521）。
const DefaultChecksumModulus = 1 << 16

// RollingHasher 是块长固定的滚动弱核对器。
// 每个块开始时调用 Reset，随后逐字节 Write；块边界上不能把旧字节滚进新块，
// 所以这里按“固定块内累加”的方式实现可增量更新的多项式和。
type RollingHasher struct {
	modulus uint32
	a       uint32
	b       uint32
	n       int
}

// NewRollingHasher 创建一个弱核对器。modulus 必须不小于 2；传 0 使用默认模数。
func NewRollingHasher(modulus uint32) *RollingHasher {
	if modulus == 0 {
		modulus = DefaultChecksumModulus
	}
	return &RollingHasher{modulus: modulus}
}

// Reset 清空一个块内的全部状态。块与块之间必须调用，避免跨块串数据。
func (h *RollingHasher) Reset() {
	h.a, h.b, h.n = 0, 0, 0
}

// Write 累加一个块内的字节。
func (h *RollingHasher) Write(p []byte) {
	m := h.modulus
	a, b := h.a, h.b
	for _, c := range p {
		a = (a + uint32(c)) % m
		b = (b + a) % m
	}
	h.a, h.b = a, b
	h.n += len(p)
}

// Sum32 返回当前块的 16 位风格弱值 (a<<16 | b) 对模数取模后的结果。
func (h *RollingHasher) Sum32() uint32 {
	return (h.a<<16 | h.b) % h.modulus
}

// Len 返回当前块已经写入的字节数。
func (h *RollingHasher) Len() int { return h.n }

// WeakSum 直接计算一块完整数据的弱值，给对端/测试使用。
func WeakSum(modulus uint32, block []byte) uint32 {
	h := NewRollingHasher(modulus)
	h.Write(block)
	return h.Sum32()
}

// StrongSum 返回一块数据的强核对值（SHA-256）。
func StrongSum(block []byte) [32]byte {
	return sha256.Sum256(block)
}
