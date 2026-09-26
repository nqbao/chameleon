package main

import (
	"context"
	"fmt"
	"os"

	chameleon "github.com/nqbao/chameleon"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	result, err := (chameleon.Options{BaseEnv: os.Environ()}).Run(context.Background(), chameleon.Request{Runtime: "shell", Dir: dir, Prompt: "printf hello"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(result.ExitCode)
	}
	fmt.Println(result.Content)
}
