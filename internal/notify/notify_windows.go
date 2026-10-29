package notify

import (
	"os"
	"os/exec"
	"syscall"
)

// toast shows a WinRT toast through PowerShell (present on every Windows
// 10/11). Title and body travel in env vars, so nothing gets interpreted.
const toast = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$x = $t.GetElementsByTagName('text')
$x.Item(0).AppendChild($t.CreateTextNode($env:USED_TITLE)) > $null
$x.Item(1).AppendChild($t.CreateTextNode($env:USED_BODY)) > $null
$id = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($t))`

func show(title, body string) error {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", toast)
	cmd.Env = append(os.Environ(), "USED_TITLE=used: "+title, "USED_BODY="+body)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd.Run()
}
