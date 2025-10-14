package chat

import (
	"errors"
	"fmt"

	"yap/internal/config"
)

var ErrQuit = errors.New("quit")

func Run(resolved config.Config, store config.Store) error {
	var cipher Cipher
	if resolved.Secret != "" {
		var err error
		cipher, err = NewAESCipher(resolved.Secret)
		if err != nil {
			return fmt.Errorf("setup error: %w", err)
		}
	}

	session, err := NewChat(Options{
		Config: resolved,
		Cipher: cipher,
		Store:  store,
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
