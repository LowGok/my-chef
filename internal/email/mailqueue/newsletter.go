package mailqueue

import (
	"context"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// NewsletterTemplate identifies the declared newsletter purposes only.
func NewsletterTemplate(name string) bool {
	switch name {
	case NewsletterENTemplate, NewsletterFRTemplate, NewsletterNLTemplate,
		NewsletterWithdrawalENTemplate, NewsletterWithdrawalFRTemplate, NewsletterWithdrawalNLTemplate:
		return true
	default:
		return false
	}
}

// NewsletterDispatchAllowed checks live identity and consent authority in the
// same transaction that claims the queued envelope. It never authorizes campaigns.
func NewsletterDispatchAllowed(ctx context.Context, tx *gorm.DB, row Record, now time.Time) (bool, error) {
	hash, ok := strings.CutPrefix(row.Key, "newsletter/")
	if !ok || len(hash) != 64 {
		return false, nil
	}
	_, address, err := store.EmailIdentity(row.Recipient)
	if err != nil {
		return false, nil
	}
	purpose := "confirm"
	if row.Template == NewsletterWithdrawalENTemplate || row.Template == NewsletterWithdrawalFRTemplate || row.Template == NewsletterWithdrawalNLTemplate {
		purpose = "unsubscribe"
	}
	var count int64
	err = tx.WithContext(ctx).Raw(`SELECT COUNT(*) FROM newsletter_proofs p
JOIN users u ON u.user_id=p.user_id AND u.email_key=p.email_key
JOIN newsletter_subscriptions n ON n.user_id=p.user_id AND n.email_key=p.email_key AND n.revision=p.revision
JOIN account_incarnations a ON a.user_id=p.user_id
JOIN account_mail_owners m ON m.incarnation=a.incarnation AND m.outbox_id=?
WHERE p.hash=? AND p.purpose=? AND u.email_key=? AND p.consumed_unix IS NULL
 AND (p.expires_unix IS NULL OR p.expires_unix>?)
 AND ((p.purpose='confirm' AND n.status IN ('pending','unsubscribed') AND p.request_order>n.proved_order)
   OR (p.purpose='unsubscribe' AND n.status='confirmed' AND p.request_order=n.proved_order))`,
		row.ID, hash, purpose, address, now.Unix()).Scan(&count).Error
	return count == 1, err
}

var newsletterENText = texttemplate.Must(texttemplate.New("newsletter-en").Parse("Confirm that you want to receive MyChef news:\n{{.confirm}}\n\nThis confirmation link expires at {{.expires}}.\nWithdraw your request or unsubscribe:\n{{.unsubscribe}}\n"))
var newsletterENHTML = htmltemplate.Must(htmltemplate.New("newsletter-en").Parse("<h1>Keep up with MyChef</h1><p><a href=\"{{.confirm}}\">Confirm my subscription</a></p><p>This confirmation link expires at {{.expires}}.</p><p><a href=\"{{.unsubscribe}}\">Withdraw or unsubscribe</a></p>"))
var newsletterFRText = texttemplate.Must(texttemplate.New("newsletter-fr").Parse("Confirmez votre souhait de recevoir les nouvelles de MyChef :\n{{.confirm}}\n\nCe lien de confirmation expire le {{.expires}}.\nRetirer votre demande ou vous désinscrire :\n{{.unsubscribe}}\n"))
var newsletterFRHTML = htmltemplate.Must(htmltemplate.New("newsletter-fr").Parse("<h1>Les nouvelles de MyChef</h1><p><a href=\"{{.confirm}}\">Confirmer mon inscription</a></p><p>Ce lien de confirmation expire le {{.expires}}.</p><p><a href=\"{{.unsubscribe}}\">Retirer ma demande ou me désinscrire</a></p>"))
var newsletterNLText = texttemplate.Must(texttemplate.New("newsletter-nl").Parse("Bevestig dat je nieuws van MyChef wilt ontvangen:\n{{.confirm}}\n\nDeze bevestigingslink vervalt op {{.expires}}.\nJe aanvraag intrekken of uitschrijven:\n{{.unsubscribe}}\n"))
var newsletterNLHTML = htmltemplate.Must(htmltemplate.New("newsletter-nl").Parse("<h1>Nieuws van MyChef</h1><p><a href=\"{{.confirm}}\">Mijn inschrijving bevestigen</a></p><p>Deze bevestigingslink vervalt op {{.expires}}.</p><p><a href=\"{{.unsubscribe}}\">Aanvraag intrekken of uitschrijven</a></p>"))

func newsletterTemplates(name string) (string, *texttemplate.Template, *htmltemplate.Template) {
	switch name {
	case NewsletterFRTemplate:
		return "Confirmez votre inscription à MyChef", newsletterFRText, newsletterFRHTML
	case NewsletterNLTemplate:
		return "Bevestig je inschrijving bij MyChef", newsletterNLText, newsletterNLHTML
	default:
		return "Confirm your MyChef subscription", newsletterENText, newsletterENHTML
	}
}

func newsletterWithdrawalTemplates(name string) (string, *texttemplate.Template, *htmltemplate.Template) {
	subject, body, heading, label := "Your MyChef newsletter subscription", "Your confirmed newsletter subscription is now linked to your MyChef account. You can unsubscribe here:", "Your MyChef subscription", "Unsubscribe"
	switch name {
	case NewsletterWithdrawalFRTemplate:
		subject, body, heading, label = "Votre inscription aux nouvelles de MyChef", "Votre inscription confirmée est maintenant liée à votre compte MyChef. Vous pouvez vous désinscrire ici :", "Votre inscription MyChef", "Me désinscrire"
	case NewsletterWithdrawalNLTemplate:
		subject, body, heading, label = "Je inschrijving voor MyChef-nieuws", "Je bevestigde inschrijving is nu gekoppeld aan je MyChef-account. Je kunt je hier uitschrijven:", "Je MyChef-inschrijving", "Uitschrijven"
	}
	text := texttemplate.Must(texttemplate.New(name).Parse(body + "\n{{.unsubscribe}}\n"))
	html := htmltemplate.Must(htmltemplate.New(name).Parse("<h1>" + heading + "</h1><p>" + body + "</p><p><a href=\"{{.unsubscribe}}\">" + label + "</a></p>"))
	return subject, text, html
}
