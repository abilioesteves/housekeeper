package api

import (
	"bytes"
	"fmt"
	"github.com/labbsr0x/whisper/misc"
	"github.com/labbsr0x/whisper/web/ui"
	"github.com/labbsr0x/whisper/workers"
	"html/template"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/labbsr0x/goh/gohtypes"
	whisper "github.com/labbsr0x/whisper-client/client"

	"github.com/sirupsen/logrus"

	"github.com/labbsr0x/whisper/web/api/types"
	"github.com/labbsr0x/whisper/web/config"
)

// UserCredentialsAPI defines the available user apis
type UserCredentialsAPI interface {
	POSTHandler() http.Handler
	PUTHandler() http.Handler
	GETEmailConfirmationPageHandler(route string) http.Handler
	GETRegistrationPageHandler(route string) http.Handler
	GETUpdatePageHandler(route string) http.Handler
}

// DefaultUserCredentialsAPI holds the default implementation of the User MailWorkerAPI interface
type DefaultUserCredentialsAPI struct {
	*config.WebBuilder
}

// InitFromWebBuilder initializes the default user credentials MailWorkerAPI from a WebBuilder
func (dapi *DefaultUserCredentialsAPI) InitFromWebBuilder(builder *config.WebBuilder) *DefaultUserCredentialsAPI {
	dapi.WebBuilder = builder
	return dapi
}

func sendConfirmationEmail(r *http.Request, payload *types.AddUserCredentialRequestPayload, baseUIPath string) {
	token, err := misc.GenerateToken(jwt.MapClaims{
		"sub":       payload.Username,                        // Subject
		"exp":       time.Now().Add(10 * time.Minute).Unix(), // Expiration
		"challenge": payload.Challenge,                       // Login Challenge
		"emt":       true,                                    // Email Confirmation Token
		"iat":       time.Now().Unix(),
	})

	gohtypes.PanicIfError("Not possible to create token", http.StatusInternalServerError, err)
	logrus.Infof("Email confirmation token created: %v", token)

	to := []string{payload.Email}
	link := "localhost:7070/email-confirmation?email_confirmation_token=" + token
	content := fmt.Sprintf("Hi %v,\nClick on the link below to authenticate your email.\n\n %v\n\nThanks,\nWhisper Developers\n", payload.Username, link)

	workers.Mail.Send(to, []byte(content))
}

// POSTHandler handles post requests to create user credentials
func (dapi *DefaultUserCredentialsAPI) POSTHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := new(types.AddUserCredentialRequestPayload).InitFromRequest(r)
		userID, err := dapi.UserCredentialsDAO.CreateUserCredential(payload.Username, payload.Password, payload.Email, false)

		gohtypes.PanicIfError("Not possible to create user", http.StatusInternalServerError, err)
		logrus.Infof("User created: %v", userID)

		go sendConfirmationEmail(r, payload, dapi.BaseUIPath)

		w.WriteHeader(200)
	})
}

// PUTHandler handles put requests to update user credentials
func (dapi *DefaultUserCredentialsAPI) PUTHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := new(types.UpdateUserCredentialRequestPayload).InitFromRequest(r)

		if token, ok := r.Context().Value(whisper.TokenKey).(whisper.Token); ok {
			err := dapi.UserCredentialsDAO.CheckCredentials(token.Subject, payload.OldPassword)

			if e, ok := err.(*gohtypes.Error); ok {
				gohtypes.Panic(e.Message, e.Code)
			}

			err = dapi.UserCredentialsDAO.UpdateUserCredential(token.Subject, payload.Email, payload.NewPassword, true)
			gohtypes.PanicIfError("Error updating user credential info", 500, err)

			w.WriteHeader(200)
		}
	})
}

// GETRegistrationPageHandler builds the page where new credentials will be inserted
func (dapi *DefaultUserCredentialsAPI) GETRegistrationPageHandler(route string) http.Handler {
	return http.StripPrefix(route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challenge, err := url.QueryUnescape(r.URL.Query().Get("login_challenge"))
		gohtypes.PanicIfError("Unable to parse the login_challenge parameter", 400, err)
		page := types.RegistrationPage{LoginChallenge: challenge}

		buf := new(bytes.Buffer)
		_ = template.Must(template.ParseFiles(path.Join(dapi.BaseUIPath, "registration.html"))).Execute(buf, page)
		html, _ := ioutil.ReadAll(buf)

		page.HTML = template.HTML(html)

		tmpl := template.Must(template.ParseFiles(path.Join(dapi.BaseUIPath, "index.html")))
		_ = tmpl.Execute(w, page)
	}))
}

func extractInfoFromEmailConfirmationToken(claims jwt.MapClaims) (challenge string, username string) {
	emt, ok := claims["emt"].(bool)
	if !ok || !emt {
		gohtypes.Panic("Email confirmation token not valid", http.StatusNotAcceptable)
	}

	username, ok = claims["sub"].(string)
	if !ok {
		gohtypes.Panic("Unable to find the user", http.StatusNotFound)
	}

	challenge, ok = claims["challenge"].(string)
	if !ok {
		gohtypes.Panic("Unable to find the login challenge", http.StatusNotFound)
	}

	return
}

func getRedirectionLink(challenge, username string, api *DefaultUserCredentialsAPI) string {
	if len(challenge) > 0 {
		payload := whisper.AcceptLoginRequestPayload{ACR: "0", Remember: false, Subject: username}
		info := api.Self.AcceptLoginRequest(challenge, payload)
		if info == nil {
			gohtypes.Panic("Unable to accept token login request", http.StatusInternalServerError)
		}

		return info["redirect_to"].(string)
	}

	return "/login"
}

// GETEmailConfirmationPageHandler builds the page where new credentials will be inserted
func (dapi *DefaultUserCredentialsAPI) GETEmailConfirmationPageHandler(route string) http.Handler {
	return http.StripPrefix(route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LoadErrorPage := func() {
			if rec := recover(); rec != nil {
				errorPage := types.EmailConfirmationPage{Successful: false, Message: rec.(gohtypes.Error).Message}
				ui.LoadPage(dapi.BaseUIPath, "email_confirmation.html", &errorPage, w)
			}
		}

		defer LoadErrorPage()

		claims := misc.ExtractTokenFromRequest(r)
		challenge, username := extractInfoFromEmailConfirmationToken(claims)

		dapi.UserCredentialsDAO.AuthenticateUserCredential(username)

		link := getRedirectionLink(challenge, username, dapi)
		page := types.EmailConfirmationPage{Successful: true, Message: "Your email has been confirmed", RedirectTo: link}

		ui.LoadPage(dapi.BaseUIPath, "email_confirmation.html", &page, w)
	}))
}

// GETUpdatePageHandler builds the page where credentials will be updated
func (dapi *DefaultUserCredentialsAPI) GETUpdatePageHandler(route string) http.Handler {
	return http.StripPrefix(route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTo, err := url.QueryUnescape(r.URL.Query().Get("redirect_to"))
		gohtypes.PanicIfError("Unable to parse the redirect_to parameter", 400, err)

		page := types.UpdatePage{RedirectTo: redirectTo}
		if token, ok := r.Context().Value(whisper.TokenKey).(whisper.Token); ok {
			userCredentials, err := dapi.UserCredentialsDAO.GetUserCredential(token.Subject)
			gohtypes.PanicIfError(fmt.Sprintf("Could not find credentials with username '%v'", token.Subject), 500, err)

			page.Username = userCredentials.Username
			page.Email = userCredentials.Email

			buf := new(bytes.Buffer)
			err = template.Must(template.ParseFiles(path.Join(dapi.BaseUIPath, "update.html"))).Execute(buf, page)
			gohtypes.PanicIfError("Error building update page", http.StatusInternalServerError, err)

			html, _ := ioutil.ReadAll(buf)
			page.HTML = template.HTML(html)

			template.Must(template.ParseFiles(path.Join(dapi.BaseUIPath, "index.html"))).Execute(w, page)
			return
		}
		gohtypes.Panic("Unauthorized: token not found", 401)
	}))
}
