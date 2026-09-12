package mailtools

import (
	"encoding/json"
	"github.com/domainry/domainry-connector-sdk/mailwrite"
	sdk "github.com/domainry/domainry-tools-sdk"
)

const WriteAccountsKey = "mail_write_accounts"

func WriteDefinitions() []sdk.Definition {
	str := func(max int, required bool) any {
		s := map[string]any{"type": "string", "maxLength": max}
		if required {
			s["minLength"] = 1
		}
		return s
	}
	object := func(fields map[string]any, required ...string) any {
		return map[string]any{"type": "object", "properties": fields, "required": append([]string{}, required...), "additionalProperties": false}
	}
	address := object(map[string]any{"address": str(512, true), "name": str(512, false)}, "address")
	recipients := map[string]any{"type": "array", "maxItems": 50, "items": address}
	message := object(map[string]any{"to": recipients, "cc": recipients, "bcc": recipients, "subject": str(2048, false), "text": str(65536, false)}, "to", "cc", "bcc", "subject", "text")
	envelope := func(request any) any {
		return object(map[string]any{"account_key": str(1024, true), "account_updated_at": str(128, true), "request": request}, "account_key", "account_updated_at", "request")
	}
	definition := func(key, action, description, effect string, input any) sdk.Definition {
		raw, _ := json.Marshal(input)
		idem := "natural"
		if effect == "write" {
			idem = "reconcile"
		}
		return sdk.Definition{Key: key, Version: "1", ActionKey: "mail." + action, Description: description, Effect: effect, Idempotency: idem, InputSchema: raw, OutputSchema: json.RawMessage(`{"type":"object"}`), TimeoutMillis: 90000, MaxOutputBytes: 131072}
	}
	return []sdk.Definition{
		definition(WriteAccountsKey, "write_accounts", "Discover accounts currently authorized to send or reply. Copy key and account_updated_at exactly. Follow next_cursor. Read access and a local draft do not authorize sending.", "read", object(map[string]any{"operation": map[string]any{"type": "string", "enum": []string{mailwrite.SendOperationKey, mailwrite.ReplyOperationKey}}, "cursor": str(512, true), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10}})),
		definition(mailwrite.SendOperationKey, "send", "Send the exact approved plain-text message from the selected account. Explicit To/CC/BCC arrays are required, including empty groups; no From, attachments, MIME or hidden recipients. Provider acceptance is not delivery. Do not retry an uncertain send under a new call.", "write", envelope(object(map[string]any{"message": message}, "message"))),
		definition(mailwrite.ReplyOperationKey, "reply", "Reply to an exact original message after approval. Read the original first; use its Reply-To (otherwise From) and matching subject. Explicitly list every additional To/CC/BCC recipient; never infer reply-all. The provider rechecks the original. An accepted receipt does not prove delivery.", "write", envelope(object(map[string]any{"message_id": str(2048, true), "message": message}, "message_id", "message"))),
	}
}
