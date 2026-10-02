package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/xray"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protowire"
)

// Новые пользователи добавляются в работающий Xray через его API, без перезапуска:
// перезапуск рвёт соединения всех остальных. Всё остальное (удаление и блокировка,
// смена ключа, настройки) по-прежнему применяется перезапуском — иначе уже открытые
// соединения заблокированного пользователя продолжили бы работать.

// hotUser — пользователь, которого нужно добавить во входящее подключение tag
type hotUser struct {
	Tag      string
	Protocol string
	Client   map[string]any
}

type inboundCfg struct {
	Tag      string         `json:"tag"`
	Protocol string         `json:"protocol"`
	Settings map[string]any `json:"settings"`
}

// hotAdds сравнивает старый и новый конфиг. ok — новый отличается от старого только
// добавленными пользователями (их и возвращает); иначе нужен перезапуск.
func hotAdds(oldCfg, newCfg []byte) (adds []hotUser, ok bool) {
	var o, n map[string]any
	if json.Unmarshal(oldCfg, &o) != nil || json.Unmarshal(newCfg, &n) != nil {
		return nil, false
	}
	oldClients, oldRest, err1 := splitClients(o)
	newClients, newRest, err2 := splitClients(n)
	if err1 != nil || err2 != nil || !bytes.Equal(oldRest, newRest) {
		return nil, false
	}
	for tag, nc := range newClients {
		oc := oldClients[tag]
		for email, client := range nc.byEmail {
			prev, existed := oc.byEmail[email]
			switch {
			case !existed:
				adds = append(adds, hotUser{Tag: tag, Protocol: nc.protocol, Client: client.raw})
			case prev.key != client.key:
				return nil, false // ключ или flow поменялись
			}
		}
		for email := range oc.byEmail {
			if _, still := nc.byEmail[email]; !still {
				return nil, false // пользователь удалён или заблокирован
			}
		}
	}
	return adds, true
}

type clientEntry struct {
	raw map[string]any
	key string // всё содержимое — чтобы заметить смену ключа
}

type inboundClients struct {
	protocol string
	byEmail  map[string]clientEntry
}

// splitClients вынимает пользователей из входящих подключений. rest — конфиг без них:
// если он не изменился, разница только в пользователях.
func splitClients(cfg map[string]any) (map[string]inboundClients, []byte, error) {
	out := map[string]inboundClients{}
	raw, _ := json.Marshal(cfg["inbounds"])
	var inbounds []map[string]any
	if err := json.Unmarshal(raw, &inbounds); err != nil {
		return nil, nil, err
	}
	for _, in := range inbounds {
		settings, _ := in["settings"].(map[string]any)
		list, has := settings["clients"].([]any)
		if !has {
			continue
		}
		tag, _ := in["tag"].(string)
		proto, _ := in["protocol"].(string)
		ic := inboundClients{protocol: proto, byEmail: map[string]clientEntry{}}
		for _, c := range list {
			m, _ := c.(map[string]any)
			email, _ := m["email"].(string)
			if email == "" {
				return nil, nil, fmt.Errorf("пользователь без email во входящем %s", tag)
			}
			key, _ := json.Marshal(m)
			ic.byEmail[email] = clientEntry{raw: m, key: string(key)}
		}
		out[tag] = ic
		delete(settings, "clients")
	}
	cfg["inbounds"] = inbounds
	rest, err := json.Marshal(cfg) // ключи map сериализуются в порядке сортировки
	return out, rest, err
}

// Запрос к API Xray кодируем сами. Пакеты app/proxyman/command, proxy/vless и
// proxy/hysteria тянут в агента весь сетевой код Xray (+4 МБ), а нужны от них
// несколько полей protobuf. Номера полей — из .proto в xray-core:
//
//	AlterInboundRequest { string tag = 1; TypedMessage operation = 2; }
//	AddUserOperation    { User user = 1; }
//	User                { uint32 level = 1; string email = 2; TypedMessage account = 3; }
//	TypedMessage        { string type = 1; bytes value = 2; }
//	vless.Account       { string id = 1; string flow = 2; string encryption = 3; }
//	hysteria Account    { string auth = 1; }
const alterInboundMethod = "/xray.app.proxyman.command.HandlerService/AlterInbound"

func pbString(b []byte, num protowire.Number, v string) []byte {
	if v == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, v)
}

func pbBytes(b []byte, num protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func typedMessage(typ string, value []byte) []byte {
	return pbBytes(pbString(nil, 1, typ), 2, value)
}

// account — учётная запись Xray (TypedMessage) для пользователя из конфига
func (h hotUser) account() ([]byte, error) {
	str := func(k string) string { s, _ := h.Client[k].(string); return s }
	switch h.Protocol {
	case "vless":
		acc := pbString(pbString(pbString(nil, 1, str("id")), 2, str("flow")), 3, "none")
		return typedMessage("xray.proxy.vless.Account", acc), nil
	case "hysteria":
		return typedMessage("xray.proxy.hysteria.account.Account", pbString(nil, 1, str("auth"))), nil
	}
	return nil, fmt.Errorf("добавление на лету не поддерживается для %s", h.Protocol)
}

// addUserRequest — AlterInboundRequest с AddUserOperation
func (h hotUser) addUserRequest() ([]byte, error) {
	acc, err := h.account()
	if err != nil {
		return nil, err
	}
	email, _ := h.Client["email"].(string)
	user := pbBytes(pbString(nil, 2, email), 3, acc)
	op := typedMessage("xray.app.proxyman.command.AddUserOperation", pbBytes(nil, 1, user))
	return pbBytes(pbString(nil, 1, h.Tag), 2, op), nil
}

// rawCodec передаёт уже закодированный protobuf как есть; ответ (пустой) не разбираем
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) { return *(v.(*[]byte)), nil }
func (rawCodec) Unmarshal([]byte, any) error   { return nil }
func (rawCodec) Name() string                  { return "proto" }

// addUsers добавляет пользователей в работающий Xray (в тестах подменяется)
func (a *agent) addUsers(adds []hotUser) error {
	if a.hotAdd != nil {
		return a.hotAdd(adds)
	}
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", xray.APIPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})))
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, u := range adds {
		req, err := u.addUserRequest()
		if err != nil {
			return err
		}
		var reply []byte
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = conn.Invoke(ctx, alterInboundMethod, &req, &reply)
		cancel()
		if err != nil {
			email, _ := u.Client["email"].(string)
			return fmt.Errorf("%s в %s: %w", email, u.Tag, err)
		}
	}
	return nil
}
