package bls

import (
	"crypto/rand"
	"testing"
)

func mkTriples(n int) (pubs, sigs, msgs [][]byte) {
	for i := 0; i < n; i++ {
		kp := GenerateKeyPair()
		m := make([]byte, 32)
		_, _ = rand.Read(m)
		pubs = append(pubs, kp.PublicKey().Bytes())
		sigs = append(sigs, Sign(kp.PrivateKey(), m).Bytes())
		msgs = append(msgs, m)
	}
	return
}

func TestVerifyBatch(t *testing.T) {
	Init()
	pubs, sigs, msgs := mkTriples(50)
	if !VerifyBatch(pubs, sigs, msgs) {
		t.Fatal("valid batch rejected")
	}
	// swap two signatures: each individually invalid, but their AGGREGATE is unchanged — batch must still reject
	sigs[3], sigs[7] = sigs[7], sigs[3]
	if VerifyBatch(pubs, sigs, msgs) {
		t.Fatal("batch accepted signatures that only cancel out in the aggregate")
	}
	sigs[3], sigs[7] = sigs[7], sigs[3]
	msgs[10] = append([]byte{}, msgs[10]...)
	msgs[10][0] ^= 1
	if VerifyBatch(pubs, sigs, msgs) {
		t.Fatal("batch accepted a wrong message")
	}
	if VerifyBatch(pubs, [][]byte{{1, 2, 3}}, msgs) || VerifyBatch(nil, nil, nil) {
		t.Fatal("malformed input must be rejected")
	}
}

func BenchmarkBatch256(b *testing.B) {
	Init()
	pubs, sigs, msgs := mkTriples(256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !VerifyBatch(pubs, sigs, msgs) {
			b.Fatal("bad")
		}
	}
}

func BenchmarkIndividual256(b *testing.B) {
	Init()
	pubs, sigs, msgs := mkTriples(256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range pubs {
			if !VerifyCompressedForBench(pubs[j], sigs[j], msgs[j]) {
				b.Fatal("bad")
			}
		}
	}
}

func VerifyCompressedForBench(pub, sig, msg []byte) bool {
	return new(blstSignature).VerifyCompressed(sig, true, pub, false, msg, dstMinPk)
}
