// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/coordinationview"
)

func TestCoordinationCheckoutClientReadsTheDocument(t *testing.T) {
	doc := coordinationview.CheckoutDocument{APIVersion: coordinationview.CheckoutAPIVersion, Kind: coordinationview.CheckoutKind}
	doc.ParentTask = "parent1x"
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/coordinations/parent1x/checkout" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "nope", status)
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer srv.Close()
	c := Client{URL: srv.URL, HC: srv.Client(), Token: func(context.Context) (string, error) { return "t", nil }}
	got, err := c.CoordinationCheckout(t.Context(), "parent1x")
	if err != nil || got.ParentTask != "parent1x" {
		t.Fatalf("%+v %v", got, err)
	}
	for code, want := range map[int]string{http.StatusNotFound: "cannot read every child", http.StatusConflict: "not every child is at one commit"} {
		status = code
		if _, err := c.CoordinationCheckout(t.Context(), "parent1x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%d: %v", code, err)
		}
	}
	status = http.StatusOK
	doc.APIVersion = "coordination-checkout/v2"
	if _, err := c.CoordinationCheckout(t.Context(), "parent1x"); err == nil {
		t.Fatal("an unknown document version was accepted")
	}
}
