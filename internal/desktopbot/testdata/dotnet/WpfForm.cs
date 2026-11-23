// A WPF data-entry form for the desktop recorder tests, built in code with the
// .NET Framework compiler that ships with Windows (see frameworks_windows_test.go).
// WPF moves the keyboard focus into an open drop-down list, which is a popup
// window of its own, and a drop-down that does not take typed text has no
// value, only a selection.
//
//	wpf-form.exe <saves.jsonl> <window title>
//
// Kaydet (also Enter, the default button) appends the fields as a JSON line.
using System;
using System.IO;
using System.Text;
using System.Windows;
using System.Windows.Controls;

static class Program {
    static string Esc(string s) {
        return s.Replace("\\", "\\\\").Replace("\"", "\\\"").Replace("\r", "\\r").Replace("\n", "\\n");
    }

    [STAThread]
    static void Main(string[] args) {
        string saves = args[0];
        var grid = new Grid { Margin = new Thickness(12) };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(110) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(320) });
        for (int i = 0; i < 6; i++) grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        Action<string, FrameworkElement, int> row = (label, c, r) => {
            var l = new Label { Content = label };
            Grid.SetRow(l, r);
            grid.Children.Add(l);
            c.Margin = new Thickness(0, 4, 0, 4);
            Grid.SetRow(c, r);
            Grid.SetColumn(c, 1);
            grid.Children.Add(c);
        };
        var name = new TextBox { Name = "txtHasta" };
        row("Hasta adı:", name, 0);
        var note = new TextBox { Name = "txtNot", Height = 60, AcceptsReturn = true, TextWrapping = TextWrapping.Wrap };
        row("Not:", note, 1);
        var pay = new ComboBox { Name = "cmbOdeme" };
        foreach (var s in new[] { "Nakit", "Kart", "Havale" }) pay.Items.Add(new ComboBoxItem { Content = s });
        row("Ödeme:", pay, 2);
        var city = new ComboBox { Name = "cmbSehir", IsEditable = true };
        foreach (var s in new[] { "Ankara", "İstanbul", "İzmir" }) city.Items.Add(new ComboBoxItem { Content = s });
        row("Şehir:", city, 3);
        var save = new Button { Name = "btnKaydet", Content = "Kaydet", Width = 120, HorizontalAlignment = HorizontalAlignment.Left, IsDefault = true };
        row("", save, 4);
        var status = new TextBlock { Name = "lblDurum", Text = "Hazır" };
        Grid.SetRow(status, 5);
        Grid.SetColumnSpan(status, 2);
        grid.Children.Add(status);

        int count = 0;
        save.Click += (s, e) => {
            count++;
            var chosen = pay.SelectedItem as ComboBoxItem;
            File.AppendAllText(saves, "{\"name\":\"" + Esc(name.Text) + "\",\"note\":\"" + Esc(note.Text) +
                "\",\"payment\":\"" + Esc(chosen == null ? "" : (string)chosen.Content) + "\",\"city\":\"" + Esc(city.Text) + "\"}\n", new UTF8Encoding(false));
            status.Text = "Kaydedildi: " + count;
        };
        var w = new Window { Title = args[1], Content = grid, SizeToContent = SizeToContent.WidthAndHeight, Topmost = true, ResizeMode = ResizeMode.NoResize };
        new Application().Run(w);
    }
}
