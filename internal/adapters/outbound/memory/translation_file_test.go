package memory_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/claudioed/network-fulfillment/internal/adapters/outbound/memory"
	"github.com/claudioed/network-fulfillment/internal/domain/shared"
)

func writeTranslations(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "products.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return path
}

func TestLoadProductTranslationFile_MakesTranslationWork(t *testing.T) {
	path := writeTranslations(t, `{
	  "products": [
	    {"networkProductId": "ASIN-1", "sku": "sku-aaa"},
	    {"networkProductId": "ASIN-2", "sku": "sku-bbb"}
	  ]
	}`)

	tr := memory.NewProductTranslation()
	n, err := memory.LoadProductTranslationFile(tr, path)
	if err != nil {
		t.Fatalf("LoadProductTranslationFile: %v", err)
	}
	if n != 2 {
		t.Fatalf("loaded %d mappings, want 2", n)
	}

	sku, err := tr.ToSKU(t.Context(), "ASIN-1")
	if err != nil {
		t.Fatalf("ToSKU: %v", err)
	}
	if sku != shared.SKU("sku-aaa") {
		t.Fatalf("ToSKU(ASIN-1) = %q, want sku-aaa", sku)
	}
}

// An unmapped product must still be ErrUnknownProduct: loading a
// dictionary must not turn a genuine gap into a fabricated SKU.
func TestLoadProductTranslationFile_UnmappedProductStillFails(t *testing.T) {
	path := writeTranslations(t, `{"products":[{"networkProductId":"ASIN-1","sku":"sku-aaa"}]}`)
	tr := memory.NewProductTranslation()
	if _, err := memory.LoadProductTranslationFile(tr, path); err != nil {
		t.Fatalf("load: %v", err)
	}

	if _, err := tr.ToSKU(t.Context(), "ASIN-NOPE"); err == nil {
		t.Fatal("an unmapped product must remain an error")
	}
}

func TestLoadProductTranslationFile_FailuresAreReturned(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"empty product id", `{"products":[{"networkProductId":"","sku":"s"}]}`, "networkProductId must not be empty"},
		{"empty sku", `{"products":[{"networkProductId":"ASIN-1","sku":""}]}`, "sku must not be empty"},
		{"duplicate mapping", `{"products":[{"networkProductId":"ASIN-1","sku":"a"},{"networkProductId":"ASIN-1","sku":"b"}]}`, "mapped twice"},
		{"unknown field", `{"products":[{"networkProductId":"ASIN-1","sku":"a","asin":"x"}]}`, "asin"},
		{"malformed json", `{"products":[`, "parse product translation file"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := memory.NewProductTranslation()
			_, err := memory.LoadProductTranslationFile(tr, writeTranslations(t, tc.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A bad entry must leave NOTHING loaded. A half-loaded dictionary would
// translate some lines of an order and reject others, and the network's
// protocol cannot express a partial acknowledgement.
func TestLoadProductTranslationFile_PartialFailureLoadsNothing(t *testing.T) {
	path := writeTranslations(t, `{
	  "products": [
	    {"networkProductId": "ASIN-GOOD", "sku": "sku-good"},
	    {"networkProductId": "ASIN-BAD", "sku": ""}
	  ]
	}`)

	tr := memory.NewProductTranslation()
	if _, err := memory.LoadProductTranslationFile(tr, path); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := tr.ToSKU(t.Context(), "ASIN-GOOD"); err == nil {
		t.Fatal("the valid entry before the bad one was loaded; a half-loaded dictionary can partially translate an order")
	}
}

func TestLoadProductTranslationFile_MissingFileIsAnError(t *testing.T) {
	tr := memory.NewProductTranslation()
	_, err := memory.LoadProductTranslationFile(tr, filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("a missing file must be an error, not a silent empty dictionary")
	}
}
