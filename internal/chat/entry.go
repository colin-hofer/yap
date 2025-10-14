package chat

import (
	"errors"
	"fmt"

	"yap/internal/config"
)

// errQuit signals that the user requested termination.
var errQuit = errors.New("quit")

// Run initialises the chat session and drives the terminal UI lifecycle.
func Run(resolved config.Config, store config.Store) error {
	var cipher packetCipher
	if resolved.Secret != "" {
		var err error
		cipher, err = newAESCipher(resolved.Secret)
		if err != nil {
			return fmt.Errorf("setup error: %w", err)
		}
	}

	session, err := newSession(sessionOptions{
		config: resolved,
		cipher: cipher,
		store:  store,
	})
	if err != nil {
		return err
	}

	session.start()
	if err := runBubbleUI(resolved.Name, session.eventStream(), session.submit); err != nil && !errors.Is(err, errQuit) {
		return fmt.Errorf("ui error: %w", err)
	}
	return session.shutdown()
}
