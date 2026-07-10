package cmd

import (
	"reflect"
	"testing"
)

func TestNewAuthManagerRegistersGeminiAuthenticator(t *testing.T) {
	manager := newAuthManager()
	authenticators := reflect.ValueOf(manager).Elem().FieldByName("authenticators")
	if !authenticators.MapIndex(reflect.ValueOf("gemini")).IsValid() {
		t.Fatal("Gemini authenticator is not registered")
	}
}
