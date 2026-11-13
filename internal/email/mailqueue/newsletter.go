package mailqueue

import (
	htmltemplate "html/template"
	texttemplate "text/template"
)

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
