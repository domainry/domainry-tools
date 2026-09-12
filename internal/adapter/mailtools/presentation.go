package mailtools

import (
	"fmt"
	"unicode/utf8"

	mail "github.com/domainry/domainry-connector-sdk/mail"
)

func shortText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// Lists have bounded header previews. Recipient addresses and source IDs are
// never shortened into a different target; omitted recipients are explicit.
func boundedSummary(in mail.Summary, recipientLimit int) mail.Summary {
	out := in
	out.Subject = shortText(in.Subject, 1024)
	out.MetadataComplete = out.MetadataComplete && out.Subject == in.Subject
	for _, target := range []struct {
		source []mail.Address
		out    *[]mail.Address
	}{{in.From, &out.From}, {in.To, &out.To}, {in.CC, &out.CC}, {in.ReplyTo, &out.ReplyTo}} {
		list := target.source
		if len(list) > recipientLimit {
			list = list[:recipientLimit]
			out.MetadataComplete = false
		}
		*target.out = append([]mail.Address{}, list...)
		for i := range *target.out {
			name := shortText((*target.out)[i].Name, 128)
			out.MetadataComplete = out.MetadataComplete && name == (*target.out)[i].Name
			(*target.out)[i].Name = name
		}
	}
	return out
}

func presentPage(raw []byte, limit int, syntax string) (any, error) {
	var page mail.MessagesPage
	if decode(raw, &page) != nil || page.Validate(limit) != nil || syntax != "" && page.QuerySyntax != syntax {
		return nil, fmt.Errorf("invalid mail page source")
	}
	for i, item := range page.Items {
		page.Items[i] = boundedSummary(item, 10)
	}
	return page, nil
}
func presentMessage(raw []byte, r mail.ReadRequest) (any, error) {
	var message mail.Message
	if decode(raw, &message) != nil || message.Validate(r) != nil {
		return nil, fmt.Errorf("invalid mail message source")
	}
	message.Summary = boundedSummary(message.Summary, 20)
	return message, nil
}
