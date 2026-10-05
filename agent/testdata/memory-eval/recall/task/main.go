package main

import "strings"

func normalize(v string) string { return v }
func split(v string) []string   { return strings.Split(v, ",") }
func join(v []string) string    { return strings.Join(v, ",") }
func main()                     {}
