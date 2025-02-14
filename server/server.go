package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	cloudBaseURLTemplate string = "https://%s.secretservercloud.%s/"
	defaultAPIPathURI    string = "/api/v1"
	defaultTokenPathURI  string = "/oauth2/token"
	defaultTLD           string = "com"
)

// UserCredential holds the username and password or access token that the API should use to
// authenticate to the REST API
type UserCredential struct {
	AccessToken, Domain, Username, Password string
}

// Configuration settings for the API
type Configuration struct {
	Credentials                                      UserCredential
	ServerURL, TLD, Tenant, apiPathURI, tokenPathURI string
	TLSClientConfig                                  *tls.Config
	LogLevel                                         LogLevel
}

// Server provides access to secrets stored in Delinea Secret Server
type Server struct {
	Configuration
}

// New returns an initialized Secrets object
func New(config Configuration) (*Server, error) {
	if config.ServerURL == "" && config.Tenant == "" || config.ServerURL != "" && config.Tenant != "" {
		return nil, fmt.Errorf("either ServerURL or Tenant must be set")
	}
	if config.TLD == "" {
		config.TLD = defaultTLD
	}
	if config.TLSClientConfig != nil {
		http.DefaultTransport.(*http.Transport).TLSClientConfig = config.TLSClientConfig
	}
	if config.apiPathURI == "" {
		config.apiPathURI = defaultAPIPathURI
	}
	config.apiPathURI = strings.Trim(config.apiPathURI, "/")
	if config.tokenPathURI == "" {
		config.tokenPathURI = defaultTokenPathURI
	}
	config.tokenPathURI = strings.Trim(config.tokenPathURI, "/")

	switch config.LogLevel {
	case LevelTrace:
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	case LevelDebug:
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case LevelWarn:
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case LevelError:
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	return &Server{config}, nil
}

// urlFor is the URL for the given resource and path
func (s Server) urlFor(resource, path string) string {
	var baseURL string

	if s.ServerURL == "" {
		baseURL = fmt.Sprintf(cloudBaseURLTemplate, s.Tenant, s.TLD)
	} else {
		baseURL = s.ServerURL
	}

	switch {
	case resource == "token":
		return fmt.Sprintf("%s/%s",
			strings.Trim(baseURL, "/"),
			strings.Trim(s.tokenPathURI, "/"))
	default:
		return fmt.Sprintf("%s/%s/%s/%s",
			strings.Trim(baseURL, "/"),
			strings.Trim(s.apiPathURI, "/"),
			strings.Trim(resource, "/"),
			strings.Trim(path, "/"))
	}
}

// accessResource uses the accessToken to access the API resource.
// It assumes an appropriate combination of method, resource, path and input.
func (s Server) accessResource(method, resource, path string, input interface{}) ([]byte, error) {
	switch resource {
	case "secrets":
	case "secret-templates":
	default:
		message := "unknown resource"

		log.Error().Msgf("%s: %s", message, resource)
		return nil, fmt.Errorf(message)
	}

	body := bytes.NewBuffer([]byte{})

	if input != nil {
		if data, err := json.Marshal(input); err == nil {
			body = bytes.NewBuffer(data)
		} else {
			log.Error().Err(err).Msg("marshaling the request body to JSON")
			return nil, err
		}
	}

	req, err := http.NewRequest(method, s.urlFor(resource, path), body)
	if err != nil {
		log.Error().Msgf("creating req: %s /%s/%s: %s", method, resource, path, err)
		return nil, err
	}

	accessToken, err := s.getAccessToken()
	if err != nil {
		log.Error().Err(err).Msg("error getting accessToken")
		return nil, err
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)

	switch method {
	case "POST", "PUT", "PATCH":
		req.Header.Set("Content-Type", "application/json")
	}

	log.Debug().Msgf("calling %s %s", method, req.URL.String())

	data, _, err := handleResponse((&http.Client{}).Do(req))

	return data, err
}

// uploadFile uploads the file described in the given fileField to the
// secret at the given secretId as a multipart/form-data request.
func (s Server) uploadFile(secretId int, fileField SecretField) error {
	log.Debug().Msgf("uploading a file to the '%s' field with filename '%s'", fileField.Slug, fileField.Filename)
	body := bytes.NewBuffer([]byte{})
	path := fmt.Sprintf("%d/fields/%s", secretId, fileField.Slug)

	// Fetch the access token
	accessToken, err := s.getAccessToken()
	if err != nil {
		log.Error().Err(err).Msg("error getting accessToken")
		return err
	}

	// Create the multipart form
	multipartWriter := multipart.NewWriter(body)
	filename := fileField.Filename
	if filename == "" {
		filename = "File.txt"
		log.Debug().Msgf("field has no filename, setting its filename to '%s'", filename)
	} else if match, _ := regexp.Match("[^.]+\\.\\w+$", []byte(filename)); !match {
		filename = filename + ".txt"
		log.Debug().Msgf("field has no filename extension, setting its filename to '%s'", filename)
	}
	form, err := multipartWriter.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	_, err = io.Copy(form, strings.NewReader(fileField.ItemValue))
	if err != nil {
		return err
	}
	err = multipartWriter.Close()
	if err != nil {
		return err
	}

	// Make the request
	req, err := http.NewRequest("PUT", s.urlFor(resource, path), body)
	if err != nil {
		return err
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	log.Debug().Msgf("uploading file with PUT %s", req.URL.String())
	_, _, err = handleResponse((&http.Client{}).Do(req))

	return err
}

// getAccessToken gets an OAuth2 Access Grant and returns the token
// endpoint and get an accessGrant.
func (s Server) getAccessToken() (string, error) {
	if s.Credentials.AccessToken != "" {
		return s.Credentials.AccessToken, nil
	}

	values := url.Values{
		"username":   {s.Credentials.Username},
		"password":   {s.Credentials.Password},
		"grant_type": {"password"},
	}
	if s.Credentials.Domain != "" {
		values["domain"] = []string{s.Credentials.Domain}
	}

	body := strings.NewReader(values.Encode())
	data, _, err := handleResponse(http.Post(s.urlFor("token", ""), "application/x-www-form-urlencoded", body))
	if err != nil {
		log.Error().Err(err).Msg("grant response error")
		return "", err
	}

	grant := struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}{}

	if err = json.Unmarshal(data, &grant); err != nil {
		log.Error().Err(err).Msg("parsing grant response")
		return "", err
	}

	return grant.AccessToken, nil
}

type LogLevel int

const (
	LevelTrace LogLevel = -2
	LevelDebug LogLevel = -1
	LevelInfo  LogLevel = 0
	LevelWarn  LogLevel = 1
	LevelError LogLevel = 2
)
