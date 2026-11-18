Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[Windows.Forms.Application]::EnableVisualStyles()

function Show-SetupOptions($Existing) {
    $form = New-Object Windows.Forms.Form
    $form.Text = 'Установка Local Broadcast'
    $form.Size = New-Object Drawing.Size(630, 590)
    $form.StartPosition = 'CenterScreen'
    $form.FormBorderStyle = 'FixedDialog'
    $form.MaximizeBox = $false
    $form.Font = New-Object Drawing.Font('Segoe UI', 10)
    function Add-Label([string]$Text, [int]$Y) {
        $label = New-Object Windows.Forms.Label
        $label.Text = $Text; $label.Location = New-Object Drawing.Point(22, $Y)
        $label.Size = New-Object Drawing.Size(535, 25); $form.Controls.Add($label)
    }
    Add-Label 'Выберите назначение этого компьютера' 20
    $role = New-Object Windows.Forms.ComboBox
    $role.Location = New-Object Drawing.Point(24, 50); $role.Width = 530; $role.DropDownStyle = 'DropDownList'
    $modes = @('master', 'presenter', 'camera', 'camera-view', 'viewer')
    $role.Items.AddRange(@('Главный компьютер с OBS', 'Презентер — экран и системный звук', 'Камера', 'Камера + просмотр презентера', 'Только просмотр'))
    $role.SelectedIndex = 0
    if ($Existing) { $index = [array]::IndexOf($modes, [string]$Existing.mode); if ($index -ge 0) { $role.SelectedIndex = $index } }
    $form.Controls.Add($role)
    Add-Label 'Название компьютера в системе' 92
    $name = New-Object Windows.Forms.TextBox
    $name.Location = New-Object Drawing.Point(24, 120); $name.Width = 530; $name.Text = $env:COMPUTERNAME
    if ($Existing) { $name.Text = $Existing.name }; $form.Controls.Add($name)
    Add-Label 'Общий ключ с главного ПК (на главном создаётся автоматически)' 162
    $secret = New-Object Windows.Forms.TextBox
    $secret.Location = New-Object Drawing.Point(24, 190); $secret.Width = 530; $secret.UseSystemPasswordChar = $true
    if ($Existing) { $secret.Text = $Existing.secret }; $form.Controls.Add($secret)
    Add-Label 'IP главного ПК (можно оставить пустым для автообнаружения)' 232
    $ip = New-Object Windows.Forms.TextBox
    $ip.Location = New-Object Drawing.Point(24, 260); $ip.Width = 530
    if ($Existing) { $ip.Text = $Existing.masterIp }; $form.Controls.Add($ip)
    $auto = New-Object Windows.Forms.CheckBox
    $auto.Text = 'Запускать автоматически при входе в Windows'; $auto.Checked = $true
    $auto.Location = New-Object Drawing.Point(24, 306); $auto.Size = New-Object Drawing.Size(530, 28)
    if ($Existing) { $auto.Checked = $Existing.launchAtLogin }; $form.Controls.Add($auto)
    $private = New-Object Windows.Forms.CheckBox
    $private.Text = 'Доверенная LAN: сделать Ethernet/Wi-Fi частной сетью'
    $private.Checked = $true; $private.Location = New-Object Drawing.Point(24, 339)
    $private.Size = New-Object Drawing.Size(580, 28); $form.Controls.Add($private)
    Add-Label 'Нужен интернет. FFmpeg/FFplay и OBS подготовятся автоматически.' 381
    Add-Label 'Windows запросит права администратора. Настройки сохранятся.' 406
    Add-Label 'Local Broadcast будет закрыт на время обновления.' 431
    $install = New-Object Windows.Forms.Button
    $install.Text = 'Установить и запустить'; $install.Location = New-Object Drawing.Point(285, 482); $install.Size = New-Object Drawing.Size(270, 36)
    $install.Add_Click({
        if ([string]::IsNullOrWhiteSpace($name.Text) -or $name.Text.Trim().Length -gt 72) {
            [Windows.Forms.MessageBox]::Show('Введите название компьютера (1–72 символа).') | Out-Null; return
        }
        if ($role.SelectedIndex -ne 0 -and $secret.Text.Trim().Length -lt 16) {
            [Windows.Forms.MessageBox]::Show('Скопируйте общий ключ из настроек главного ПК (не менее 16 символов).') | Out-Null; return
        }
        if ($role.SelectedIndex -eq 0 -and $secret.Text.Trim().Length -gt 0 -and $secret.Text.Trim().Length -lt 16) {
            [Windows.Forms.MessageBox]::Show('Ключ должен содержать не менее 16 символов, либо оставьте поле пустым.') | Out-Null; return
        }
        if ($ip.Text.Trim()) {
            $parsed = $null
            if (![Net.IPAddress]::TryParse($ip.Text.Trim(), [ref]$parsed) -or $parsed.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork) {
                [Windows.Forms.MessageBox]::Show('Введите IPv4 главного ПК или оставьте поле пустым.') | Out-Null; return
            }
        }
        $form.DialogResult = [Windows.Forms.DialogResult]::OK; $form.Close()
    })
    $form.Controls.Add($install); $form.AcceptButton = $install
    $role.Add_SelectedIndexChanged({ $ip.Enabled = $role.SelectedIndex -ne 0 })
    $ip.Enabled = $role.SelectedIndex -ne 0
    if ($form.ShowDialog() -ne [Windows.Forms.DialogResult]::OK) { $form.Dispose(); return $null }
    $options = [pscustomobject]@{ Mode=$modes[$role.SelectedIndex]; Name=$name.Text.Trim(); Secret=$secret.Text.Trim(); MasterIP=$ip.Text.Trim(); AutoRun=$auto.Checked; PrivateNetwork=$private.Checked }
    $form.Dispose()
    return $options
}

function Wait-SetupClosed([string]$ProcessName, [string]$Description) {
    $session = (Get-Process -Id $PID).SessionId
    while (Get-Process -Name $ProcessName -ErrorAction SilentlyContinue | Where-Object { $_.SessionId -eq $session }) {
        $choice = [Windows.Forms.MessageBox]::Show(('Закройте ' + $Description + ' перед установкой и нажмите «Повторить». Настройки будут сохранены.'), 'Local Broadcast', 'RetryCancel', 'Information')
        if ($choice -ne [Windows.Forms.DialogResult]::Retry) { throw 'Установка отменена: приложение продолжает работать.' }
    }
}

function Select-SetupCamera([string[]]$Devices) {
    if ($Devices.Count -eq 1) { return $Devices[0] }
    if (!$Devices.Count) { throw 'Камера не найдена. Подключите камеру, разрешите доступ к ней в Windows и повторите установку.' }
    $form = New-Object Windows.Forms.Form
    $form.Text = 'Выберите камеру'; $form.Size = New-Object Drawing.Size(550, 160); $form.StartPosition = 'CenterScreen'
    $list = New-Object Windows.Forms.ComboBox
    $list.Location = New-Object Drawing.Point(20, 20); $list.Width = 490; $list.DropDownStyle = 'DropDownList'; $list.Items.AddRange($Devices); $list.SelectedIndex = 0
    $button = New-Object Windows.Forms.Button
    $button.Text = 'Продолжить'; $button.Location = New-Object Drawing.Point(365, 65); $button.Width = 145; $button.DialogResult = 'OK'
    $form.Controls.AddRange(@($list, $button)); $form.AcceptButton = $button
    if ($form.ShowDialog() -ne 'OK') { $form.Dispose(); throw 'Выбор камеры отменён.' }
    $selected = [string]$list.SelectedItem; $form.Dispose(); return $selected
}
