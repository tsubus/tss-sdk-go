package main

import (
	"fmt"
	"os"

	"github.com/DelineaXPM/tss-sdk-go/v2/server"
	"github.com/rs/zerolog/log"
)

func main() {
	var userCredential server.UserCredential

	accessToken := os.Getenv("TSS_TOKEN")
	if accessToken == "" {
		userCredential = server.UserCredential{
			Username: os.Getenv("TSS_USERNAME"),
			Password: os.Getenv("TSS_PASSWORD"),
		}
	} else {
		userCredential = server.UserCredential{
			AccessToken: accessToken,
		}
	}

	tss, err := server.New(server.Configuration{
		Credentials: userCredential,
		Tenant:      os.Getenv("TSS_TENANT"),
	})
	if err != nil {
		log.Fatal().Err(err).Msg("error initializing the server configuration")
	}

	s, err := tss.Secret(1)
	if err != nil {
		log.Fatal().Err(err).Msg("error calling server.Secret")
	}

	if pw, ok := s.Field("password"); ok {
		fmt.Print("the password is", pw)
	}
}
