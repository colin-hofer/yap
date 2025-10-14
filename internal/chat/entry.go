package chat

import (
	"errors"
	"fmt"
)

var ErrQuit = errors.New("quit")

func Run(args []string) error {
	resolved, store, err := ResolveArgs(args)
	if err != nil {
		return err
	}

	var cipher Cipher
	if resolved.Secret != "" {
		cipher, err = NewAESCipher(resolved.Secret)
		if err != nil {
			return fmt.Errorf("setup error: %w", err)
		}
	}

	session, err := NewChat(Options{
		Name:    resolved.Name,
		Listen:  resolved.Listen,
		Secret:  resolved.Secret,
		Peers:   resolved.Peers,
		Network: UDPNetwork{},
		Cipher:  cipher,
		Config:  store,
	})
	if err != nil {
		return err
	}

	session.Start()
	if err := RunBubbleUI(resolved.Name, session.Events(), session.Submit); err != nil && !errors.Is(err, ErrQuit) {
		return fmt.Errorf("ui error: %w", err)
	}
	return session.Shutdown()
}
