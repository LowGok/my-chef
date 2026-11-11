package mailqueue

import (
	"bytes"
	"errors"
	htmltemplate "html/template"
	"net/mail"
	"strconv"
	texttemplate "text/template"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

type Message struct {
	Subject string
	Text    string
	HTML    string
}

var smokeText = texttemplate.Must(texttemplate.New("smoke").Parse("MyChef email test\nTest {{.testID}} at {{.timestamp}}.\n"))
var smokeHTML = htmltemplate.Must(htmltemplate.New("smoke").Parse("<h1>MyChef email test</h1><p>Test {{.testID}} at {{.timestamp}}.</p>"))

const AccountDeletedTemplate = store.AccountDeletedTemplate

var deletedText = texttemplate.Must(texttemplate.New("account-deleted").Parse("Your MyChef account was deleted at {{.timestamp}}.\nYour live account data has been removed. If you did not request this, reply to this email for help. Deletion cannot be undone.\n"))
var deletedHTML = htmltemplate.Must(htmltemplate.New("account-deleted").Parse("<h1>Your MyChef account was deleted</h1><p>Time: {{.timestamp}}</p><p>Your live account data has been removed. If you did not request this, reply for help. Deletion cannot be undone.</p>"))

const FeedbackTemplate = "feedback-operator/v1"
const PasswordChangedTemplate = "password-changed/v1"
const EmailChangedTemplate = "email-changed/v1"
const RecoveryProofTemplate = "password-recovery/v1"

var changedText = texttemplate.Must(texttemplate.New("password-changed").Parse(
	"Your MyChef password changed at {{.timestamp}}.\nIf you did not make this change, reply to this email for help.\n",
))
var changedHTML = htmltemplate.Must(htmltemplate.New("password-changed").Parse(
	"<h1>Your MyChef password changed</h1><p>Time: {{.timestamp}}</p><p>If you did not make this change, reply to this email for help.</p>",
))

var recoveryText = texttemplate.Must(texttemplate.New("password-recovery").Parse(
	"Reset your MyChef password:\n{{.link}}\n\nThis link expires at {{.expires}}. If you did not request it, you can ignore this email. Reply if you need help.\n",
))
var recoveryHTML = htmltemplate.Must(htmltemplate.New("password-recovery").Parse(
	"<h1>Reset your MyChef password</h1><p><a href=\"{{.link}}\">Choose a new password</a></p><p>This link expires at {{.expires}}. If you did not request it, you can ignore this email. Reply if you need help.</p>",
))

var signUpText = texttemplate.Must(texttemplate.New("signup-proof").Parse(
	"Finish creating your MyChef account:\n{{.link}}\n\nThis link expires at {{.expires}}. If you did not request it, you can ignore this email. Reply if you need help.\n",
))
var signUpHTML = htmltemplate.Must(htmltemplate.New("signup-proof").Parse(
	"<h1>Finish creating your MyChef account</h1><p><a href=\"{{.link}}\">Continue sign-up</a></p><p>This link expires at {{.expires}}. If you did not request it, you can ignore this email. Reply if you need help.</p>",
))

var verifyEmailText = texttemplate.Must(texttemplate.New("email-verification").Parse(
	"Verify the email address on your MyChef account:\n{{.link}}\n\nOpen the link while signed in to the same account. The link expires at {{.expires}}. If you did not request it, you can ignore this email.\n",
))
var verifyEmailHTML = htmltemplate.Must(htmltemplate.New("email-verification").Parse(
	"<h1>Verify your MyChef email address</h1><p><a href=\"{{.link}}\">Verify email</a></p><p>Open the link while signed in to the same account. The link expires at {{.expires}}. If you did not request it, you can ignore this email.</p>",
))
var emailChangeCurrentText = texttemplate.Must(texttemplate.New("email-change-current").Parse(
	"Review a request to change the email address on your MyChef account.\nReview: {{.approve}}\nCancel: {{.cancel}}\n\nThese links expire at {{.expires}}. Your email stays the same until both inboxes approve.\n",
))
var emailChangeCurrentHTML = htmltemplate.Must(htmltemplate.New("email-change-current").Parse(
	"<h1>Review your MyChef email change</h1><p><a href=\"{{.approve}}\">Review and approve</a> or <a href=\"{{.cancel}}\">Cancel the request</a>.</p><p>These links expire at {{.expires}}. Your email stays the same until both inboxes approve.</p>",
))
var emailChangeProposedText = texttemplate.Must(texttemplate.New("email-change-proposed").Parse(
	"Confirm this email address for a MyChef account:\n{{.link}}\n\nThis link expires at {{.expires}}. If you did not request it, you can ignore this email.\n",
))
var emailChangeProposedHTML = htmltemplate.Must(htmltemplate.New("email-change-proposed").Parse(
	"<h1>Confirm your proposed MyChef email address</h1><p><a href=\"{{.link}}\">Confirm email</a></p><p>This link expires at {{.expires}}. If you did not request it, you can ignore this email.</p>",
))
var emailChangedText = texttemplate.Must(texttemplate.New("email-changed").Parse(
	"The sign-in email for your MyChef account changed at {{.timestamp}}. All previous sessions ended. If you did not make this change, reply for help.\n",
))
var emailChangedHTML = htmltemplate.Must(htmltemplate.New("email-changed").Parse(
	"<h1>Your MyChef sign-in email changed</h1><p>Time: {{.timestamp}}. All previous sessions ended.</p><p>If you did not make this change, reply for help.</p>",
))

var feedbackText = texttemplate.Must(texttemplate.New("feedback").Parse(
	"Reference: {{.reference}}\nContact reason: {{.topic}}\nTime: {{.timestamp}}\nSupport: {{.priority}}\nContact: {{.contact}}\n{{if .recipe}}Recipe reference: {{.recipe}}\n{{end}}\n{{.message}}\n",
))
var feedbackHTML = htmltemplate.Must(htmltemplate.New("feedback").Parse(
	"<h1>MyChef beta feedback</h1><p>Reference: {{.reference}}</p><p>Contact reason: {{.topic}}</p><p>Time: {{.timestamp}}</p><p>Support: {{.priority}}</p><p>Contact: {{.contact}}</p>{{if .recipe}}<p>Recipe reference: {{.recipe}}</p>{{end}}<pre>{{.message}}</pre>",
))

func Render(name string, fields map[string]string) (Message, error) {
	var subject string
	var text *texttemplate.Template
	var html *htmltemplate.Template
	switch name {
	case "provider-smoke-test/v1":
		if len(fields) != 2 || fields["testID"] == "" || len(fields["testID"]) > 128 {
			return Message{}, errors.New("invalid email template or fields")
		}
		subject, text, html = "MyChef email test", smokeText, smokeHTML
	case FeedbackTemplate:
		address, err := mail.ParseAddress(fields["contact"])
		if err != nil || address.Address != fields["contact"] || len(fields["reference"]) != 32 ||
			fields["message"] == "" || (fields["priority"] != "standard" && fields["priority"] != "priority") {
			return Message{}, errors.New("invalid email template or fields")
		}
		topic := map[string]string{
			"technical_issue": "Technical issue", "recipe_issue": "Recipe issue", "subscription": "My subscription", "other": "Other",
		}[fields["topic"]]
		if topic == "" {
			return Message{}, errors.New("invalid email template or fields")
		}
		fields = map[string]string{
			"reference": fields["reference"], "timestamp": fields["timestamp"], "topic": topic,
			"priority": fields["priority"], "contact": fields["contact"], "message": fields["message"], "recipe": fields["recipe"],
		}
		subject, text, html = "MyChef beta feedback", feedbackText, feedbackHTML
	case AccountDeletedTemplate:
		if len(fields) != 1 {
			return Message{}, errors.New("invalid email template or fields")
		}
		subject, text, html = "Your MyChef account was deleted", deletedText, deletedHTML
	case PasswordChangedTemplate:
		if len(fields) != 1 {
			return Message{}, errors.New("invalid email template or fields")
		}
		subject, text, html = "Your MyChef password changed", changedText, changedHTML
	case RecoveryProofTemplate:
		if !validProofFields(name, fields, 1) {
			return Message{}, ErrProofUnavailable
		}
		subject, text, html = "Reset your MyChef password", recoveryText, recoveryHTML
	case SignUpProofTemplate:
		if len(fields) != 2 || !validProofLink(fields["link"]) {
			return Message{}, ErrProofUnavailable
		}
		subject, text, html = "Finish creating your MyChef account", signUpText, signUpHTML
	case EmailVerificationTemplate:
		if len(fields) != 2 || !validProofLink(fields["link"]) {
			return Message{}, ErrProofUnavailable
		}
		subject, text, html = "Verify your MyChef email address", verifyEmailText, verifyEmailHTML
	case EmailChangeCurrentTemplate:
		if !validProofFields(name, fields, 1) {
			return Message{}, ErrProofUnavailable
		}
		subject, text, html = "Review your MyChef email change", emailChangeCurrentText, emailChangeCurrentHTML
	case EmailChangeProposedTemplate:
		if !validProofFields(name, fields, 1) {
			return Message{}, ErrProofUnavailable
		}
		subject, text, html = "Confirm your proposed MyChef email address", emailChangeProposedText, emailChangeProposedHTML
	case EmailChangedTemplate:
		if len(fields) != 1 {
			return Message{}, errors.New("invalid email template or fields")
		}
		subject, text, html = "Your MyChef sign-in email changed", emailChangedText, emailChangedHTML
	default:
		return Message{}, errors.New("invalid email template or fields")
	}
	timestampField := "timestamp"
	if ProtectedTemplate(name) {
		timestampField = "expires"
	}
	stamp, err := time.Parse(time.RFC3339, fields[timestampField])
	if err != nil || stamp.UTC().Format(time.RFC3339) != fields[timestampField] {
		return Message{}, errors.New("invalid email timestamp")
	}
	var plain, markup bytes.Buffer
	if err := text.Execute(&plain, fields); err != nil {
		return Message{}, errors.New("could not render email text")
	}
	if err := html.Execute(&markup, fields); err != nil {
		return Message{}, errors.New("could not render email HTML")
	}
	return Message{Subject: subject, Text: plain.String(), HTML: markup.String()}, nil
}

func FeedbackRequest(recipient string, notice store.FeedbackNotice) Request {
	fields := map[string]string{
		"reference": notice.Reference, "timestamp": time.Unix(notice.CreatedUnix, 0).UTC().Format(time.RFC3339),
		"topic": notice.Topic, "priority": notice.Priority, "message": notice.Message, "contact": notice.ContactEmail,
	}
	if notice.RecipeID != nil {
		fields["recipe"] = store.FeedbackRecipeSource + ":" + strconv.FormatInt(*notice.RecipeID, 10)
	}
	return Request{OwnerID: notice.OwnerID, Key: "feedback/" + notice.Reference, Recipient: recipient, Template: FeedbackTemplate, Fields: fields}
}
