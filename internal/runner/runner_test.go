package runner

import (
	"strings"
	"testing"
)

func TestAnsibleFailure(t *testing.T) {
	out := `PLAY [Full Deploy VPN Node] ****
TASK [Проверка переменных] ****
fatal: [31.56.211.247]: UNREACHABLE! => {"changed": false, "msg": "Invalid/incorrect password: Permission denied, please try again.", "unreachable": true}
PLAY RECAP ****`
	got := ansibleFailure(out)
	if !strings.Contains(got, "Invalid/incorrect password") || strings.Contains(got, "PLAY RECAP") {
		t.Fatalf("ожидали только строку с причиной, получили %q", got)
	}
	if ansibleFailure("всё хорошо") != "" {
		t.Fatal("без fatal причины быть не должно")
	}
}

func TestAnsibleEnvForgetsHostKeys(t *testing.T) {
	env := strings.Join(ansibleEnv(), "\n")
	for _, want := range []string{"ANSIBLE_HOST_KEY_CHECKING=False", "UserKnownHostsFile=/dev/null"} {
		if !strings.Contains(env, want) {
			t.Fatalf("нет %q: переустановленный сервер со сменившимся ключом не пустит по паролю", want)
		}
	}
}
