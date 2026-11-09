package main

// Unit tests for Connection.fingerprint: the session-pool key must not embed
// the password and must not be forgeable by values that contain the separator.

import (
	"strings"
	"testing"
)

func TestFingerprintDoesNotEmbedPassword(t *testing.T) {
	c := Connection{Driver: "mysql", Host: "h", Port: "3306", User: "u", Database: "d", Password: "s3cret"}
	fp := c.fingerprint()
	if strings.Contains(fp, "s3cret") {
		t.Fatalf("fingerprint leaked the plaintext password: %q", fp)
	}
	// Same password still maps to the same session; a different one must not.
	if other := c.fingerprint(); other != fp {
		t.Fatalf("fingerprint is not deterministic: %q vs %q", fp, other)
	}
	c2 := c
	c2.Password = "different"
	if c2.fingerprint() == fp {
		t.Fatal("a changed password must produce a different fingerprint")
	}
}

func TestFingerprintLengthPrefixedAgainstCollision(t *testing.T) {
	// The classic delimiter-forgery pair: without length prefixes both would
	// join to the same string.
	a := Connection{Driver: "mysql", Host: "h", Port: "1", User: "a|b", Database: "c"}
	b := Connection{Driver: "mysql", Host: "h", Port: "1", User: "a", Database: "b|c"}
	if a.fingerprint() == b.fingerprint() {
		t.Fatal("values containing the separator must not collide")
	}
}

func TestFingerprintDistinguishesEveryField(t *testing.T) {
	base := Connection{Driver: "mysql", Host: "h", Port: "3306", User: "u", Database: "d", Password: "p"}
	ref := base.fingerprint()
	mutations := []func(*Connection){
		func(c *Connection) { c.Driver = "postgres" },
		func(c *Connection) { c.Host = "other" },
		func(c *Connection) { c.Port = "5432" },
		func(c *Connection) { c.User = "other" },
		func(c *Connection) { c.Database = "other" },
		func(c *Connection) { c.Password = "other" },
		func(c *Connection) { c.SSH.Enabled = true },
		func(c *Connection) { c.SSH.Host = "bastion" },
		func(c *Connection) { c.SSH.Port = "2222" },
		func(c *Connection) { c.SSH.User = "deploy" },
		func(c *Connection) { c.SSH.AuthMode = "key" },
	}
	for i, mutate := range mutations {
		c := base
		mutate(&c)
		if c.fingerprint() == ref {
			t.Errorf("mutation %d did not change the fingerprint", i)
		}
	}
}
