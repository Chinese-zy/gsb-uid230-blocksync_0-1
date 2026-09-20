package blocksync

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const testBlockSize = 16
const testModulus = 251 // 小模数，弱值空间小，方便在固定字节上找到真实碰撞

// fixedBlock 生成“序号 i 的确定性固定字节块”，全部用它，不引入随机。
func fixedBlock(i int) []byte {
	b := make([]byte, testBlockSize)
	for j := range b {
		b[j] = byte((i*7 + j*13 + 1) % 256)
	}
	return b
}

func findCollisionPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	seen := map[uint32][]byte{}
	for i := 0; i < 100000; i++ {
		b := fixedBlock(i)
		w := WeakSum(testModulus, b)
		if other, ok := seen[w]; ok && !bytes.Equal(other, b) {
			if StrongSum(other) != StrongSum(b) {
				return other, b
			}
		}
		seen[w] = b
	}
	t.Fatal("没在固定字节空间里找到弱核对撞对")
	return nil, nil
}

func TestFixedBlocksRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir, testBlockSize, nil)
	if err != nil {
		t.Fatal(err)
	}

	const n = 10
	data := make([]byte, 0, n*testBlockSize)
	want := make([][]byte, n)
	for i := 0; i < n; i++ {
		want[i] = fixedBlock(i)
		data = append(data, want[i]...)
	}

	var sent [][]byte
	send := func(index int, block []byte) error {
		sent = append(sent, append([]byte(nil), block...))
		return st.Append(index, block)
	}

	// 对端为空：所有块都应按序发送并落盘。
	res, err := RunSync(data, testBlockSize, testModulus,
		PeerSums{BlockSize: testBlockSize}, send)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != n || res.Reused != 0 {
		t.Fatalf("空对端：Sent=%d Reused=%d, 想要 %d/0", res.Sent, res.Reused, n)
	}

	got, err := st.Blocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("落盘块数=%d, 想要 %d", len(got), n)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("块 %d 内容与原始顺序不一致", i)
		}
	}
}

func TestWeakCollisionRequiresStrong(t *testing.T) {
	a, b := findCollisionPair(t)

	wa, wb := WeakSum(testModulus, a), WeakSum(testModulus, b)
	if wa != wb {
		t.Fatal("搜索出的两块弱值竟然不同")
	}
	if StrongSum(a) == StrongSum(b) {
		t.Fatal("两块不同却强值相同，测试数据有问题")
	}

	// 对端只有 a；源端当前块是 b。弱核对命中 a，但强值不同 => 撞上，必须切开重传。
	peer := PeerSums{
		BlockSize: testBlockSize,
		Weak:      []uint32{wa},
		Strong:    [][32]byte{StrongSum(a)},
	}

	var got []byte
	sender := func(index int, block []byte) error {
		if index != 0 {
			t.Fatalf("发送块序号=%d, 想要 0", index)
		}
		got = append([]byte(nil), block...)
		return nil
	}

	res, err := RunSync(b, testBlockSize, testModulus, peer, sender)
	if err != nil {
		t.Fatal(err)
	}
	if res.Collisions != 1 || res.Reused != 0 || res.Sent != 1 {
		t.Fatalf("碰撞统计 Collisions=%d Reused=%d Sent=%d, 想要 1/0/1",
			res.Collisions, res.Reused, res.Sent)
	}
	if !bytes.Equal(got, b) {
		t.Fatal("弱碰撞时没有把当前块原样切开重传")
	}

	// 反过来：对端有 a，源端也是 a => 两级核对一致，直接复用、不发送。
	sendCalled := false
	res, err = RunSync(a, testBlockSize, testModulus, peer, func(int, []byte) error {
		sendCalled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendCalled || res.Reused != 1 || res.Sent != 0 {
		t.Fatalf("同一块应被复用：called=%v Reused=%d Sent=%d",
			sendCalled, res.Reused, res.Sent)
	}
}

func TestMixedReuseAndSend(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir, testBlockSize, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 对端先有第 0、1 块。
	peerBlocks := [][]byte{fixedBlock(0), fixedBlock(1)}
	for i, b := range peerBlocks {
		if err := st.Append(i, b); err != nil {
			t.Fatal(err)
		}
	}

	// 源端 4 块：前两块对端已有（复用），后两块缺失（按序发送）。
	data := append(append([]byte{}, fixedBlock(0)...), fixedBlock(1)...)
	data = append(data, fixedBlock(2)...)
	data = append(data, fixedBlock(3)...)

	peer, err := PeerSumsFromStore(st, testModulus)
	if err != nil {
		t.Fatal(err)
	}

	var sentIdx []int
	send := func(index int, block []byte) error {
		sentIdx = append(sentIdx, index)
		return st.Append(index, block)
	}
	res, err := RunSync(data, testBlockSize, testModulus, peer, send)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reused != 2 || res.Sent != 2 {
		t.Fatalf("Reused=%d Sent=%d, 想要 2/2", res.Reused, res.Sent)
	}
	if len(sentIdx) != 2 || sentIdx[0] != 2 || sentIdx[1] != 3 {
		t.Fatalf("发送序号=%v, 想要 [2 3]", sentIdx)
	}
	if st.Count() != 4 {
		t.Fatalf("最终块数=%d, 想要 4", st.Count())
	}
}

func TestFailInjectionAndRecovery(t *testing.T) {
	cases := []struct {
		name  string
		stage string
	}{
		{"写之前掐断", StageBeforeWrite},
		{"写一半掐断", StageMidWrite},
		{"改名前掐断", StageBeforeRename},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			injected := errors.New("injected crash")
			fail := func(stage string, index int) error {
				if stage == tc.stage && index == 2 {
					return injected
				}
				return nil
			}

			st, err := OpenStore(dir, testBlockSize, fail)
			if err != nil {
				t.Fatal(err)
			}

			// 块 0、1 完整落盘。
			for i := 0; i < 2; i++ {
				if err := st.Append(i, fixedBlock(i)); err != nil {
					t.Fatal(err)
				}
			}
			// 块 2 在指定阶段被掐掉。
			err = st.Append(2, fixedBlock(2))
			if !errors.Is(err, injected) {
				t.Fatalf("块2 错误=%v, 想要注入错误", err)
			}
			if st.Count() != 2 {
				t.Fatalf("被掐后块数=%d, 想要 2", st.Count())
			}

			// 不能乱序补：后面的块 3 不允许抢到块 2 前面。
			if err := st.Append(3, fixedBlock(3)); !errors.Is(err, ErrOutOfOrder) {
				t.Fatalf("块3 抢占错误=%v, 想要 ErrOutOfOrder", err)
			}

			// “重新打开进程”：不杀进程，只对同一目录做恢复重开。
			st2, err := st.Reopen()
			if err != nil {
				t.Fatal(err)
			}
			if st2.Count() != 2 {
				t.Fatalf("重开后块数=%d, 想要 2（块2半成品必须丢弃）", st2.Count())
			}

			// 盘上不应残留 .part 半成品。
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if filepath.Ext(e.Name()) == ".part" {
					t.Fatalf("重开后仍有半成品文件 %s", e.Name())
				}
			}

			// 重开后只能按原顺序把已完整块吐出来。
			got, err := st2.Blocks()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if !bytes.Equal(got[i], fixedBlock(i)) {
					t.Fatalf("重开后块 %d 内容/顺序不对", i)
				}
			}

			// 恢复后去掉失败点，从块 2 继续，最终顺序与数据完整。
			st3, err := OpenStore(dir, testBlockSize, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := 2; i < 5; i++ {
				if err := st3.Append(i, fixedBlock(i)); err != nil {
					t.Fatal(err)
				}
			}
			all, err := st3.Blocks()
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 5 {
				t.Fatalf("恢复后续传后块数=%d, 想要 5", len(all))
			}
			for i := range all {
				if !bytes.Equal(all[i], fixedBlock(i)) {
					t.Fatalf("最终块 %d 与原始顺序不一致", i)
				}
			}
		})
	}
}

func TestUnalignedAndBadBlock(t *testing.T) {
	if _, err := RunSync(make([]byte, testBlockSize+1), testBlockSize, testModulus,
		PeerSums{BlockSize: testBlockSize}, nil); !errors.Is(err, ErrUnalignedStream) {
		t.Fatalf("非整数倍长度错误=%v, 想要 ErrUnalignedStream", err)
	}

	dir := t.TempDir()
	st, err := OpenStore(dir, testBlockSize, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(0, make([]byte, testBlockSize-1)); !errors.Is(err, ErrBadBlockSize) {
		t.Fatalf("半块错误=%v, 想要 ErrBadBlockSize", err)
	}
	if err := st.Append(1, make([]byte, testBlockSize)); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("跳号错误=%v, 想要 ErrOutOfOrder", err)
	}

	// 滚动核对是块内的：同一字节模式在不同长度/块上互不串扰。
	h := NewRollingHasher(testModulus)
	h.Write([]byte{1, 2, 3, 4})
	first := h.Sum32()
	h.Reset()
	h.Write([]byte{1, 2, 3, 4})
	if h.Sum32() != first {
		t.Fatal("Reset 后同数据弱值不稳定")
	}
}

func TestExampleDocumented(t *testing.T) {
	// 一条最小用法示例，顺带保证公开 API 串起来可用。
	dir := t.TempDir()
	st, _ := OpenStore(dir, testBlockSize, nil)
	data := append(append([]byte{}, fixedBlock(0)...), fixedBlock(1)...)
	res, err := RunSync(data, testBlockSize, testModulus,
		PeerSums{BlockSize: testBlockSize}, st.Append)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("同步完成：共 %d 块，发送 %d，复用 %d，弱碰撞 %d\n",
		res.Blocks, res.Sent, res.Reused, res.Collisions)
}
