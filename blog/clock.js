/* 时钟 + 月历（首页用） */
(function () {
  var pad = function (n) { return (n < 10 ? '0' : '') + n; };
  var weeks = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'];
  function tick() {
    var now = new Date();
    var h = document.getElementById('clock-h');
    if (!h) return;
    h.textContent = pad(now.getHours());
    document.getElementById('clock-m').textContent = pad(now.getMinutes());
    document.getElementById('clock-s').textContent = pad(now.getSeconds());
    document.getElementById('clock-date').textContent =
      now.getFullYear() + ' 年 ' + (now.getMonth() + 1) + ' 月 ' + now.getDate() + ' 日';
    document.getElementById('clock-week').textContent = weeks[now.getDay()];
  }
  function calendar() {
    var now = new Date();
    var y = now.getFullYear(), m = now.getMonth(), today = now.getDate();
    var title = document.getElementById('cal-title');
    var table = document.getElementById('calendar');
    if (!title || !table) return;
    title.textContent = y + ' 年 ' + (m + 1) + ' 月';
    var first = new Date(y, m, 1).getDay();
    var days = new Date(y, m + 1, 0).getDate();
    var html = '<tr><th>日</th><th>一</th><th>二</th><th>三</th><th>四</th><th>五</th><th>六</th></tr>';
    var d = 1;
    for (var r = 0; r < 6 && d <= days; r++) {
      html += '<tr>';
      for (var c = 0; c < 7; c++) {
        if (r === 0 && c < first) { html += '<td class="dim"></td>'; }
        else if (d > days) { html += '<td class="dim"></td>'; }
        else {
          html += d === today ? '<td class="today">' + d + '</td>' : '<td>' + d + '</td>';
          d++;
        }
      }
      html += '</tr>';
    }
    table.innerHTML = html;
  }
  tick(); calendar();
  setInterval(tick, 1000);
})();
