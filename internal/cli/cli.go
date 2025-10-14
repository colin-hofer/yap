package cli

import (
	"bytes"
	"errors"
	"io"

	"yap/internal/config"
)

// CLI coordinates subcommands and forwards arguments to the chat runtime.
type CLI struct {
	in     io.Reader
	out    io.Writer
	err    io.Writer
	runner func(config.Config, config.Store) error
}

func New(in io.Reader, out io.Writer, err io.Writer, runner func(config.Config, config.Store) error) *CLI {
	return &CLI{in: in, out: out, err: err, runner: runner}
}

func (c *CLI) Run(args []string) error {
	if len(args) == 0 {
		return c.runChat(args)
	}

	switch args[0] {
	case "init":
		return c.runInit(args[1:])
	case "with":
		return c.runWith(args[1:])
	default:
		return c.runChat(args)
	}
}

func (c *CLI) runWith(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yap with <config> [flags]")
	}
	forwarded := append([]string{"-group", args[0]}, args[1:]...)
	return c.runChat(forwarded)
}

func (c *CLI) runChat(args []string) error {
	resolved, store, err := c.resolveArgs(args)
	if err != nil {
		return err
	}
	if c.runner == nil {
		return errors.New("chat runner not configured")
	}
	return c.runner(resolved, store)
}

func (c *CLI) stdin() io.Reader {
	if c.in != nil {
		return c.in
	}
	return bytes.NewReader(nil)
}

func (c *CLI) stdout() io.Writer {
	if c.out != nil {
		return c.out
	}
	return io.Discard
}

func (c *CLI) stderr() io.Writer {
	if c.err != nil {
		return c.err
	}
	return io.Discard
}
