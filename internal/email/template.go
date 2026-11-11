package email

import "github.com/LowGok/my-chef/internal/email/mailqueue"

func renderTemplate(name string, fields map[string]string) (mailqueue.Message, error) {
	return mailqueue.Render(name, fields)
}
