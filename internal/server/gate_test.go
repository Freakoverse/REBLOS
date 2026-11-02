package server

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// mineNonce finds a nonce so SHA-256("<c>:<nonce>") has >= d leading zero bits.
func mineNonce(c string, d int) string {
	for i := 0; ; i++ {
		n := strconv.Itoa(i)
		sum := sha256.Sum256([]byte(c + ":" + n))
		if leadingZeroBits(sum[:]) >= d {
			return n
		}
	}
}

func TestPowGate_ChallengeVerify(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	srv.powDifficulty = 10 // small enough to mine instantly in a test

	hash := strings.Repeat("a", 64)
	ip := "1.2.3.4"

	c := srv.powChallenge(hash, ip)
	nonce := mineNonce(c, srv.powDifficulty)
	proof := c + ":" + nonce

	if !srv.verifyPow(proof, hash, ip) {
		t.Fatal("valid proof should verify")
	}
	if srv.verifyPow(proof, hash, "9.9.9.9") {
		t.Fatal("proof bound to a different IP must fail")
	}
	if srv.verifyPow(proof, strings.Repeat("b", 64), ip) {
		t.Fatal("proof bound to a different blob must fail")
	}
	if srv.verifyPow(c+":0", hash, ip) {
		t.Fatal("wrong nonce must fail")
	}
	// tamper the challenge signature
	if srv.verifyPow(c+"x:"+nonce, hash, ip) {
		t.Fatal("tampered challenge must fail")
	}
}

func TestPowGate_Middleware(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	srv.powDifficulty = 10

	var passed bool
	h := srv.gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
		w.WriteHeader(http.StatusOK)
	}))
	hash := strings.Repeat("a", 64)
	const remote = "1.2.3.4:5555"

	// 1) no proof → 428 with a challenge header
	req := httptest.NewRequest(http.MethodGet, "/"+hash+".zip", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("want 428, got %d", w.Code)
	}
	ch := w.Header().Get("X-Blossom-Gate-Pow")
	i := strings.Index(ch, "c=")
	if i < 0 {
		t.Fatalf("no challenge in header %q", ch)
	}
	c := ch[i+2:]

	// 2) valid proof → passes through
	req2 := httptest.NewRequest(http.MethodGet, "/"+hash+".zip", nil)
	req2.RemoteAddr = remote
	req2.Header.Set("X-Blossom-Gate-Pow-Proof", c+":"+mineNonce(c, 10))
	passed = false
	h.ServeHTTP(httptest.NewRecorder(), req2)
	if !passed {
		t.Fatal("valid proof should pass through to the blob handler")
	}

	// 3) non-blob path is never gated
	passed = false
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !passed {
		t.Fatal("non-blob path should pass through untouched")
	}
}

func TestBlobHashFromPath(t *testing.T) {
	h := strings.Repeat("a", 64)
	for _, p := range []string{"/" + h, "/" + h + ".zip"} {
		if got, ok := blobHashFromPath(p); !ok || got != h {
			t.Fatalf("%s → (%q,%v)", p, got, ok)
		}
	}
	for _, p := range []string{"/", "/upload", "/not-a-hash"} {
		if _, ok := blobHashFromPath(p); ok {
			t.Fatalf("%s should not parse as a blob", p)
		}
	}
}
