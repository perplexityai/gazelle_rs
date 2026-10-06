package main

import (
	"reflect"
	"testing"
)

func TestExport(t *testing.T) {
	data := []byte(`<?xml version="1.1"?><query>
 <rule class="alias" name="@vendor//:codec"><label name="actual" value="@@impl//:codec"/></rule>
 <rule class="alias" name="@vendor//:codec-1.2.3"><label name="actual" value="@@impl//:codec"/></rule>
 <rule class="rust_library" name="@@impl//:codec"><string name="name" value="codec"/><string name="crate_name" value="wire_codec"/></rule>
 <rule class="alias" name="@vendor//:derive-2.0.0"><label name="actual" value="@@derive//:derive"/></rule>
 <rule class="rust_proc_macro" name="@@derive//:derive"><string name="name" value="wire-derive"/></rule>
 <rule class="filegroup" name="@vendor//:licenses"/>
 </query>`)
	got, err := export(data, "@vendor//:")
	if err != nil {
		t.Fatal(err)
	}
	want := catalog{Version: 1, Crates: []crate{
		{Name: "wire_codec", Label: "@vendor//:codec-1.2.3", Aliases: []string{"@vendor//:codec"}},
		{Name: "wire_derive", Label: "@vendor//:derive-2.0.0", ProcMacro: true},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
func TestIncompleteQueriesFail(t *testing.T) {
	for _, data := range []string{
		`<query><rule class="alias" name="@crates//:missing"><label name="actual" value="@@missing//:lib"/></rule></query>`,
		`<query><rule class="alias" name="@crates//:cycle"><label name="actual" value="@crates//:cycle"/></rule></query>`,
		`<query/>`,
	} {
		if _, err := export([]byte(data), "@crates//:"); err == nil {
			t.Fatalf("accepted incomplete query %s", data)
		}
	}
}
