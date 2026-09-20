package blocksync

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// memFile 是内存版 SyncFile，可在第 failAt 次 Sync 时注入失败，
// 模拟写到一半被掐掉，不用真杀进程。
type memFile struct {
	buf    []byte
	syncs  int
	failAt int // 第几次 Sync 失败，0 表示不失败
}

var errInjected = errors.New("注入的失败点")

func (m *memFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.buf)) {
		return 0, io.EOF
	}
	n := copy(p, m.buf[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *memFile) WriteAt(p []byte, off int64) (int, error) {
	end := int(off) + len(p)
	if end > len(m.buf) {
		nb := make([]byte, end)
		copy(nb, m.buf)
		m.buf = nb
	}
	copy(m.buf[off:], p)
	return len(p), nil
}

func (m *memFile) Sync() error {
	m.syncs++
	if m.failAt > 0 && m.syncs == m.failAt {
		return errInjected
	}
	return nil
}

func (m *memFile) Truncate(size int64) error {
	m.buf = m.buf[:size]
	return nil
}

func fixedBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	const blockSize = 16
	src := fixedBytes(200)
	dst := make([]byte, 200)
	copy(dst, src)
	dst[40] ^= 0xff
	dst = append(dst[:96], append(fixedBytes(16), dst[96:]...)...)

	sigs := Signature(src, blockSize)
	ops := Delta(sigs, dst, blockSize)
	got := Reconstruct(sigs, src, ops, blockSize)
	if !bytes.Equal(got, dst) {
		t.Fatalf("重建结果不一致")
	}
	copies := 0
	for _, op := range ops {
		if op.Copy {
			copies++
		}
	}
	if copies == 0 {
		t.Fatalf("滚动核对没有命中任何整块")
	}
}

func TestWeakCollisionNotMerged(t *testing.T) {
	// {0,2,0} 与 {1,0,1} 弱核对相同、内容不同。
	a := []byte{0, 2, 0}
	b := []byte{1, 0, 1}
	if weakSum(a).sum() != weakSum(b).sum() {
		t.Fatalf("测试前提不成立：两块弱核对应当相同")
	}
	if strongSum(a) == strongSum(b) {
		t.Fatalf("测试前提不成立：两块硬核对应当不同")
	}
	sigs := Signature(a, 3)
	ops := Delta(sigs, b, 3)
	for _, op := range ops {
		if op.Copy {
			t.Fatalf("弱核对撞上但硬核对不同，两块绝不能合并")
		}
	}
	if got := Reconstruct(sigs, a, ops, 3); !bytes.Equal(got, b) {
		t.Fatalf("重建结果不一致")
	}
}

func TestCrashMidWriteOnlyCompletePrefix(t *testing.T) {
	const blockSize = 8
	data := fixedBytes(blockSize * 5)
	sigs := Signature(data, blockSize)
	ops := Delta(sigs, data, blockSize)

	// 打开算 1 次 Sync，之后每块 2 次；第 6 次失败时
	// 前 2 块已提交，第 3 块写了一半被掐掉。
	f := &memFile{failAt: 6}
	st, err := OpenStore(f, blockSize)
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncTo(st, sigs, data, ops, blockSize); !errors.Is(err, errInjected) {
		t.Fatalf("应当撞上注入的失败点, got %v", err)
	}

	// 重新打开，只能吐出前 2 块，后面的块不能抢到前面。
	st2, err := OpenStore(f, blockSize)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st2.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data[:2*blockSize]) {
		t.Fatalf("崩溃后只能吐出已落完整的前缀, got %d 字节", len(got))
	}

	// 补齐剩余块后完整可见，顺序不变。
	rest := data[2*blockSize:]
	if err := SyncTo(st2, sigs, data, Delta(sigs, rest, blockSize), blockSize); err != nil {
		t.Fatal(err)
	}
	got, err = st2.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("补齐后应当得到完整数据")
	}
}

func TestTruncatedTailDiscarded(t *testing.T) {
	const blockSize = 4
	f := &memFile{}
	st, err := OpenStore(f, blockSize)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append([]byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	// 模拟第 2 块写到一半：计数没推进，尾巴留着半截记录。
	if _, err := f.WriteAt([]byte{0, 0}, int64(len(f.buf))); err != nil {
		t.Fatal(err)
	}
	st2, err := OpenStore(f, blockSize)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st2.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("半截尾巴必须被丢弃, got %v", got)
	}
}
