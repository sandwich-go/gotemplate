// Test for set template
package main

import "fmt"

// Generate a simple private set
//
// Renerate the templates with "go generate"
//
//go:generate gotemplate "github.com/sandwich-go/gotemplate/set" mySet(string)
func main() {
	s := newMySet()
	s.Add("Sausage")
	s.Add("Bacon")
	fmt.Println(s)
}
