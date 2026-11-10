/* love 站前端：账户、上传、展示效果、管理员。只调 /love/api/*。 */
(function () {
  var gallery = document.getElementById('gallery');
  var carouselBar = document.getElementById('carousel-bar');
  var carCount = document.getElementById('car-count');
  var FX = ['polaroid', 'grid', 'carousel', 'masonry', 'coverflow', 'justified', 'stack', 'scatter', 'tree', 'ring'];
  var currentFx = 'polaroid';
  var carIndex = 0;
  var carTimer = null;
  var coverIndex = 0;
  var stackIndex = 0;
  var ringAngle = 0;        // 圆环当前转角（度）
  var ringFocus = -1;       // 被点中的那张下标，-1 表示没点中
  var ringRotating = false; // 是否正在自动转
  var ringRaf = null;
  var scrollRaf = null;     // 网格/瀑布流/满幅行的上下自动滚动
  var scrollDir = 1;
  var scrollPausedUntil = 0;
  var me = null;

  function api(path, opts) {
    opts = opts || {};
    opts.headers = opts.headers || {};
    if (opts.body && typeof opts.body === 'string') {
      opts.headers['Content-Type'] = 'application/json';
    }
    return fetch(path, opts).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || ('请求失败 HTTP ' + r.status));
        return j;
      });
    });
  }

  function $(id) { return document.getElementById(id); }

  /* 密码先在浏览器里算 SHA-256 再发，请求里不再出现明文密码。
     输入是「用户名 + 换行 + 密码」，不同用户同密码也不同。
     服务端再对收到的哈希做 PBKDF2 存起来。 */
  function hashPassword(username, password) {
    var data = new TextEncoder().encode(username + '\n' + password);
    return crypto.subtle.digest('SHA-256', data).then(function (digest) {
      var bytes = new Uint8Array(digest);
      var hex = '';
      for (var i = 0; i < bytes.length; i++) {
        hex += ('0' + bytes[i].toString(16)).slice(-2);
      }
      return hex;
    });
  }

  /* 上传前先在浏览器里压图：最长边 1600px、JPEG 质量 0.82。
     手机原图动不动几 MB，压完一般 200~400KB，上传和加载都快一个量级。
     GIF 动图不动（压了就没动画了）；压完没更小就用原图。 */
  function compressImage(file) {
    if (file.type === 'image/gif') return Promise.resolve(file);
    var url = URL.createObjectURL(file);
    return new Promise(function (resolve) {
      var img = new Image();
      img.onload = function () {
        var maxDim = 1600;
        var scale = Math.min(1, maxDim / Math.max(img.naturalWidth, img.naturalHeight));
        var canvas = document.createElement('canvas');
        canvas.width = Math.max(1, Math.round(img.naturalWidth * scale));
        canvas.height = Math.max(1, Math.round(img.naturalHeight * scale));
        canvas.getContext('2d').drawImage(img, 0, 0, canvas.width, canvas.height);
        canvas.toBlob(function (blob) {
          URL.revokeObjectURL(url);
          if (!blob || blob.size >= file.size) { resolve(file); return; }
          var name = file.name.replace(/\.[^.]*$/, '') + '.jpg';
          resolve(new File([blob], name, { type: 'image/jpeg' }));
        }, 'image/jpeg', 0.82);
      };
      img.onerror = function () { URL.revokeObjectURL(url); resolve(file); };
      img.src = url;
    });
  }

  /* ---------- 展示效果 ---------- */

  function applyFx(fx) {
    if (FX.indexOf(fx) < 0) fx = 'polaroid';
    currentFx = fx;
    gallery.classList.remove('fx-polaroid', 'fx-grid', 'fx-carousel', 'fx-masonry', 'fx-coverflow', 'fx-justified', 'fx-stack', 'fx-scatter', 'fx-tree', 'fx-ring');
    gallery.classList.add('fx-' + fx);
    carouselBar.style.display = (fx === 'carousel' || fx === 'coverflow' || fx === 'stack' || fx === 'ring') ? 'flex' : 'none';
    $('car-toggle').style.display = fx === 'ring' ? '' : 'none';
    document.querySelectorAll('#fx-picker .fx-btn[data-fx]').forEach(function (b) {
      b.classList.toggle('active', b.getAttribute('data-fx') === fx);
    });
    stopAuto();
    ringStop();
    stopScroll();
    stopAdvance();
    stopMasonryScroll();
    // 离开瀑布流时把两列拆掉、卡片放回画廊根上。
    if (fx !== 'masonry') teardownMasonryCols();
    if (fx === 'carousel') {
      carIndex = 0;
      showSlide(0);
      startAuto();
    } else if (fx === 'coverflow') {
      coverIndex = 0;
      layoutCoverflow();
      startAdvance();
    } else if (fx === 'stack') {
      stackIndex = 0;
      layoutStack();
      startAdvance();
    } else if (fx === 'scatter') {
      layoutScatter();
    } else if (fx === 'tree') {
      layoutTree();
    } else if (fx === 'ring') {
      ringFocus = -1;
      ringStart();
      layoutRing();
    } else if (fx === 'masonry') {
      // 瀑布流：双列对向滚动。先清掉别的样式，再把卡片分进两列、开始对向滚动。
      gallery.querySelectorAll('.photo-card').forEach(function (c) {
        c.style.display = '';
        c.style.transform = '';
        c.style.opacity = '';
        c.style.zIndex = '';
        c.style.left = '';
        c.style.top = '';
        c.style.position = '';
        c.style.width = '';
        c.style.gridRowEnd = '';
        c.classList.remove('ring-focus');
      });
      gallery.style.height = '';
      buildMasonryCols();
      startMasonryScroll();
    } else if (fx === 'grid' || fx === 'justified') {
      // 网格/满幅行：画廊是固定高度的滚动区，上下自动滚动、来回走。
      gallery.scrollTop = 0;
      scrollDir = 1;
      // 其他效果下，把轮播/封面流/堆叠/扩散/环形/记忆树留下的行内样式清掉，卡片恢复正常流式布局。
      gallery.querySelectorAll('.photo-card').forEach(function (c) {
        c.style.display = '';
        c.style.transform = '';
        c.style.opacity = '';
        c.style.zIndex = '';
        c.style.left = '';
        c.style.top = '';
        c.style.position = '';
        c.style.width = '';
        c.style.gridRowEnd = '';
        c.classList.remove('ring-focus');
      });
      startScroll();
      gallery.style.height = '';
    } else {
      // 其他效果下，把轮播/封面流/堆叠/扩散/环形/记忆树留下的行内样式清掉，卡片恢复正常流式布局。
      gallery.querySelectorAll('.photo-card').forEach(function (c) {
        c.style.display = '';
        c.style.transform = '';
        c.style.opacity = '';
        c.style.zIndex = '';
        c.style.left = '';
        c.style.top = '';
        c.style.position = '';
        c.style.width = '';
        c.style.gridRowEnd = '';
        c.classList.remove('ring-focus');
      });
      gallery.style.height = '';
    }
  }

  /* 堆叠：一叠牌，顶上的正对着你，下面几张按顺序歪着露边。点顶上的牌翻下一张。 */
  function layoutStack() {
    var list = cards();
    if (!list.length) return;
    stackIndex = Math.max(0, Math.min(stackIndex, list.length - 1));
    list.forEach(function (c, i) {
      var behind = (i - stackIndex + list.length) % list.length; // 0 = 顶上那张
      var tilt = (behind % 2 === 0 ? 1 : -1) * Math.min(behind, 4) * 2.2;
      c.style.display = '';
      c.style.left = '';
      c.style.top = '';
      c.style.transform =
        'translate(' + (behind * 7) + 'px, ' + (behind * 5) + 'px) rotate(' + tilt + 'deg)';
      c.style.zIndex = String(list.length - behind);
      c.style.opacity = behind > 5 ? '0' : String(Math.max(0.35, 1 - behind * 0.12));
    });
    carCount.textContent = (stackIndex + 1) + ' / ' + list.length;
  }

  function stackGo(i) {
    var list = cards();
    if (!list.length) return;
    stackIndex = (i + list.length) % list.length;
    layoutStack();
    if (currentFx === 'stack') startAdvance(); // 手动翻了，5 秒计时重来
  }

  // 堆叠下点顶上的牌：翻下一张。
  gallery.addEventListener('click', function (e) {
    if (currentFx !== 'stack') return;
    var card = e.target.closest('.photo-card');
    if (!card) return;
    var idx = cards().indexOf(card);
    if (idx === stackIndex) {
      e.stopPropagation();
      stackGo(stackIndex + 1);
    }
  }, true);

  /* 环形：照片排成一个 3D 圆环，自动慢慢转；点哪张，哪张转到正前方、变大，环停下。
     半径按卡片宽和张数算（CSS 3D Lab 的环形公式），太小会挤在一起、太大太散，都夹一下。
     背过去的卡片用 backface-visibility 藏起来，只看得到前面半圈。 */
  function ringRadius(n) {
    var r = (260 / 2) / Math.tan(Math.PI / Math.max(n, 1));
    return Math.max(260, Math.min(600, r));
  }

  function layoutRing() {
    var list = cards();
    if (!list.length) return;
    var n = list.length;
    var radius = ringRadius(n);
    list.forEach(function (c, i) {
      var angle = (i / n) * 360 + ringAngle;
      var focused = (i === ringFocus);
      c.style.display = '';
      c.style.left = '';
      c.style.top = '';
      c.style.transform =
        'rotateY(' + angle.toFixed(1) + 'deg) translateZ(' + radius + 'px)' +
        (focused ? ' translateZ(90px) scale(1.28)' : '');
      // 正前方的最清楚，越往两边越淡；背过去的靠 backface 藏。
      var a = ((angle % 360) + 360) % 360;
      var dist = Math.min(a, 360 - a) / 180; // 0=正前方，1=正后方
      c.style.opacity = String(Math.max(0.25, 1 - dist * 0.75));
      c.style.zIndex = String(100 - Math.round(dist * 100));
      c.classList.toggle('ring-focus', focused);
    });
    var front = 0, best = 1e9;
    list.forEach(function (c, i) {
      var a = (((i / n) * 360 + ringAngle) % 360 + 360) % 360;
      var d = Math.min(a, 360 - a);
      if (d < best) { best = d; front = i; }
    });
    carCount.textContent = (ringFocus >= 0 ? ringFocus + 1 : front + 1) + ' / ' + n;
    $('car-toggle').textContent = ringRotating ? '⏸ 暂停' : '▶️ 继续转';
  }

  function ringTick() {
    if (!ringRotating || currentFx !== 'ring') { ringRaf = null; return; }
    ringAngle = (ringAngle + 0.12) % 360; // 约 7 度/秒，转一圈约 50 秒
    layoutRing();
    ringRaf = requestAnimationFrame(ringTick);
  }

  function ringStart() {
    if (ringRaf) return;
    ringRotating = true;
    ringRaf = requestAnimationFrame(ringTick);
    $('car-toggle').textContent = '⏸ 暂停';
  }
  function ringStop() {
    ringRotating = false;
    if (ringRaf) { cancelAnimationFrame(ringRaf); ringRaf = null; }
    $('car-toggle').textContent = '▶️ 继续转';
  }

  // 点一张：把它转到正前方、变大，环停下；再点一次正前方的那张才开灯箱。
  function ringFocusPhoto(i) {
    var list = cards();
    if (!list.length) return;
    var n = list.length;
    ringFocus = (i + n) % n;
    ringStop();
    // 把这张转到正前方（角度 0），取最短的那条路转过去。
    var target = -(ringFocus / n) * 360;
    var diff = ((target - ringAngle) % 360 + 540) % 360 - 180;
    ringAngle = (ringAngle + diff) % 360;
    layoutRing();
  }
  function ringResume() {
    ringFocus = -1;
    ringStart();
    layoutRing();
  }

  // 环形下点卡片：没点中的先聚焦（停下、变大）；已聚焦的放行给灯箱。
  gallery.addEventListener('click', function (e) {
    if (currentFx !== 'ring') return;
    var card = e.target.closest('.photo-card');
    if (!card) {
      // 点空白处：继续转。
      if (ringFocus >= 0) ringResume();
      return;
    }
    var idx = cards().indexOf(card);
    if (idx >= 0 && idx !== ringFocus) {
      e.stopPropagation();
      ringFocusPhoto(idx);
    }
  }, true);
  function layoutScatter() {
    var list = cards();
    if (!list.length) return;
    var cols = window.innerWidth < 680 ? 2 : 4;
    var colW = 100 / cols;
    var rowH = 250;
    list.forEach(function (c, i) {
      var r1 = (((i + 1) * 2654435761) % 1000) / 1000; // 0..1 伪随机
      var r2 = (((i + 1) * 40503) % 1000) / 1000;
      var col = i % cols;
      var row = Math.floor(i / cols);
      var left = col * colW + (r1 - 0.5) * 6;
      var top = row * rowH + (r2 - 0.5) * 70;
      var rot = (r1 - 0.5) * 28;
      c.style.display = '';
      c.style.opacity = '';
      c.style.zIndex = '';
      c.style.left = left + '%';
      c.style.top = top + 'px';
      c.style.transform = 'rotate(' + rot.toFixed(1) + 'deg)';
    });
    gallery.style.height = (Math.ceil(list.length / cols) * rowH + 60) + 'px';
  }

  /* 瀑布流（双列对向滚动）：照片按奇偶分进左右两列，左列往上、右列往下，
     到头了再往回走，像电影开头两条片花对着转。用户动手就停 4 秒。 */
  var masonryRaf = null;
  var masonryPausedUntil = 0;
  var masonryDirL = 1, masonryDirR = -1;
  function buildMasonryCols() {
    teardownMasonryCols();
    var list = cards();
    if (!list.length) return;
    var left = document.createElement('div');
    left.className = 'masonry-col';
    var right = document.createElement('div');
    right.className = 'masonry-col';
    list.forEach(function (c, i) {
      if (i % 2 === 0) left.appendChild(c);
      else right.appendChild(c);
    });
    gallery.appendChild(left);
    gallery.appendChild(right);
    // 右列先从中间往下走，左列从顶往上走，两边错开。
    right.scrollTop = Math.max(0, (right.scrollHeight - right.clientHeight) / 2);
    left.scrollTop = 0;
    masonryDirL = 1; masonryDirR = -1;
  }
  function teardownMasonryCols() {
    stopMasonryScroll();
    var cols = gallery.querySelectorAll('.masonry-col');
    if (!cols.length) return;
    // 把卡片放回画廊根上（恢复原顺序），再拆掉两列。
    var back = [];
    cols.forEach(function (col) {
      Array.prototype.slice.call(col.children).forEach(function (c) {
        back.push(c);
      });
    });
    back.forEach(function (c) { gallery.appendChild(c); });
    cols.forEach(function (col) { col.remove(); });
  }
  function masonryScrollTick() {
    if (currentFx !== 'masonry') { masonryRaf = null; return; }
    var cols = gallery.querySelectorAll('.masonry-col');
    if (cols.length < 2) { masonryRaf = null; return; }
    if (Date.now() < masonryPausedUntil) {
      masonryRaf = requestAnimationFrame(masonryScrollTick); return;
    }
    var left = cols[0], right = cols[1];
    [ [left, 'L'], [right, 'R'] ].forEach(function (pair) {
      var col = pair[0], side = pair[1];
      var max = col.scrollHeight - col.clientHeight;
      if (max <= 0) return;
      var dir = side === 'L' ? masonryDirL : masonryDirR;
      var next = col.scrollTop + dir * 0.6; // 约 36px/秒
      if (next >= max) { next = max; if (side === 'L') masonryDirL = -1; else masonryDirR = -1; }
      if (next <= 0) { next = 0; if (side === 'L') masonryDirL = 1; else masonryDirR = 1; }
      col.scrollTop = next;
    });
    masonryRaf = requestAnimationFrame(masonryScrollTick);
  }
  function startMasonryScroll() {
    if (masonryRaf) return;
    masonryRaf = requestAnimationFrame(masonryScrollTick);
  }
  function stopMasonryScroll() {
    if (masonryRaf) { cancelAnimationFrame(masonryRaf); masonryRaf = null; }
  }
  // 在列上动手就暂停双列滚动 4 秒。
  gallery.addEventListener('wheel', function (e) {
    if (currentFx === 'masonry' && e.target.closest('.masonry-col')) {
      masonryPausedUntil = Date.now() + 4000;
    }
  }, { passive: true });
  gallery.addEventListener('touchstart', function (e) {
    if (currentFx === 'masonry' && e.target.closest('.masonry-col')) {
      masonryPausedUntil = Date.now() + 4000;
    }
  }, { passive: true });

  /* 记忆树：照片像挂件一样挂在樱花树背景图上。按 1、2、3、4… 的行排成三角形树冠，
     每张上面一根挂绳。背景图在 CSS 里（love/tree-bg.jpg）。
     每行封顶（电脑 6 张、手机 4 张），不然 50 张照片时底下那行宽得摆不下、
     树也高得没法看；封顶后多出来的照片往下另起一行，树变高、页面能滚。 */
  function layoutTree() {
    var list = cards();
    if (!list.length) return;
    var W = gallery.clientWidth || 600;
    var isMobile = window.innerWidth < 680;
    var cardW = isMobile ? 80 : 100;
    var maxPerRow = isMobile ? 4 : 6;
    var rowH = isMobile ? 130 : 150;
    var rows = [];
    var i = 0, n = 1;
    while (i < list.length) {
      rows.push(list.slice(i, i + n));
      i += n;
      if (n < maxPerRow) n++;
    }
    var H = rows.length * rowH + 130; // 底部留树干
    gallery.style.height = H + 'px';
    rows.forEach(function (row, r) {
      var y = 40 + r * rowH;
      row.forEach(function (c, k) {
        var x;
        if (row.length === 1) x = W / 2;
        else x = (W * 0.10) + (k * (W * 0.80) / (row.length - 1));
        c.style.position = 'absolute';
        c.style.width = cardW + 'px';
        c.style.left = x + 'px';
        c.style.top = y + 'px';
        c.style.display = '';
        c.style.opacity = '';
        c.style.zIndex = '';
        c.style.transform = 'translate(-50%, 0) rotate(' + (((r * 7 + k * 13) % 11) - 5) + 'deg)';
      });
    });
  }

  /* 封面流：按 active 下标排 3D 扇形（参考 Addy Osmani 的 Cover Flow 与 CSS 3D Lab 的做法）。
     中间那张正对着你，两边依次往后退、往外倾斜、变淡。 */
  function layoutCoverflow() {
    var list = cards();
    if (!list.length) return;
    coverIndex = Math.max(0, Math.min(coverIndex, list.length - 1));
    list.forEach(function (c, i) {
      var off = i - coverIndex;
      var sign = off < 0 ? -1 : 1;
      var mag = Math.min(Math.abs(off), 3);
      c.style.display = '';
      c.style.transform =
        'translateX(' + (off * 56) + '%) translateZ(' + (-mag * 140) + 'px) rotateY(' + (-sign * mag * 34) + 'deg)';
      c.style.zIndex = String(10 - mag);
      c.style.opacity = String(Math.max(0, 1 - mag * 0.28));
    });
    carCount.textContent = (coverIndex + 1) + ' / ' + list.length;
  }

  function coverGo(i) {
    var list = cards();
    if (!list.length) return;
    coverIndex = (i + list.length) % list.length;
    layoutCoverflow();
    if (currentFx === 'coverflow') startAdvance(); // 手动切了，5 秒计时重来
  }

  // 封面流下点一张非中间的卡：先把它挪到中间，不打开灯箱；点中间那张才打开灯箱。
  gallery.addEventListener('click', function (e) {
    if (currentFx !== 'coverflow') return;
    var card = e.target.closest('.photo-card');
    if (!card) return;
    var idx = cards().indexOf(card);
    if (idx >= 0 && idx !== coverIndex) {
      e.stopPropagation();
      coverGo(idx);
    }
  }, true);

  function cards() {
    return Array.prototype.slice.call(gallery.querySelectorAll('.photo-card'));
  }

  function showSlide(i) {
    var list = cards();
    if (!list.length) return;
    carIndex = (i + list.length) % list.length;
    list.forEach(function (c, idx) {
      c.style.display = idx === carIndex ? '' : 'none';
    });
    carCount.textContent = (carIndex + 1) + ' / ' + list.length;
  }

  function startAuto() {
    stopAuto();
    carTimer = setInterval(function () { showSlide(carIndex + 1); }, 5000);
  }
  function stopAuto() {
    if (carTimer) { clearInterval(carTimer); carTimer = null; }
  }

  /* 网格/满幅行：上下自动滚动，来回走。画廊是个固定高度的滚动区，
     慢慢往下走，到底了再慢慢往上走；用户一动手（滚轮/触摸/点）就停 4 秒再继续。
     瀑布流是双列对向滚动，不走这里（有自己的 masonryScrollTick）。 */
  function scrollTick() {
    if (currentFx !== 'grid' && currentFx !== 'justified') {
      scrollRaf = null; return;
    }
    if (Date.now() < scrollPausedUntil) {
      scrollRaf = requestAnimationFrame(scrollTick); return;
    }
    var max = gallery.scrollHeight - gallery.clientHeight;
    if (max <= 0) { scrollRaf = null; return; }
    var next = gallery.scrollTop + scrollDir * 0.6; // 约 36px/秒，很慢
    if (next >= max) { next = max; scrollDir = -1; }
    if (next <= 0) { next = 0; scrollDir = 1; }
    gallery.scrollTop = next;
    scrollRaf = requestAnimationFrame(scrollTick);
  }
  function startScroll() {
    if (scrollRaf) return;
    scrollDir = 1;
    scrollRaf = requestAnimationFrame(scrollTick);
  }
  function stopScroll() {
    if (scrollRaf) { cancelAnimationFrame(scrollRaf); scrollRaf = null; }
  }
  // 用户动手就暂停自动滚动 4 秒。
  ['wheel', 'touchstart', 'pointerdown'].forEach(function (ev) {
    gallery.addEventListener(ev, function () {
      scrollPausedUntil = Date.now() + 4000;
    }, { passive: true });
  });

  /* 封面流/堆叠：每 5 秒自动切下一张（轮播本来就有 5 秒自动换，环形本来就在转）。
     点卡片手动切的时候，计时重来。 */
  var advTimer = null;
  function startAdvance() {
    stopAdvance();
    advTimer = setInterval(function () {
      if (currentFx === 'coverflow') coverGo(coverIndex + 1);
      else if (currentFx === 'stack') stackGo(stackIndex + 1);
    }, 5000);
  }
  function stopAdvance() {
    if (advTimer) { clearInterval(advTimer); advTimer = null; }
  }

  $('car-prev').addEventListener('click', function () {
    if (currentFx === 'coverflow') coverGo(coverIndex - 1);
    else if (currentFx === 'stack') stackGo(stackIndex - 1);
    else if (currentFx === 'ring') ringFocusPhoto((ringFocus >= 0 ? ringFocus : 0) - 1);
    else { showSlide(carIndex - 1); startAuto(); }
  });
  $('car-next').addEventListener('click', function () {
    if (currentFx === 'coverflow') coverGo(coverIndex + 1);
    else if (currentFx === 'stack') stackGo(stackIndex + 1);
    else if (currentFx === 'ring') ringFocusPhoto((ringFocus >= 0 ? ringFocus : 0) + 1);
    else { showSlide(carIndex + 1); startAuto(); }
  });
  $('car-toggle').addEventListener('click', function () {
    if (currentFx !== 'ring') return;
    if (ringRotating) ringStop();
    else ringResume();
  });

  document.querySelectorAll('#fx-picker .fx-btn[data-fx]').forEach(function (b) {
    b.addEventListener('click', function () {
      var fx = b.getAttribute('data-fx');
      applyFx(fx);
      if (!me) return;
      api('/love/api/prefs', {
        method: 'POST',
        body: JSON.stringify({ display_effect: fx }),
      }).catch(function (e) {
        $('upload-msg').textContent = '效果已切换，但保存失败：' + e.message;
      });
    });
  });

  /* 窗口大小变了（比如手机横竖屏切换），跟画廊宽度有关的几种效果要重排，
     不然记忆树、瀑布流这些会错位。防抖 200ms，别转一下就狂算。 */
  var resizeTimer = null;
  window.addEventListener('resize', function () {
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function () {
      if (currentFx === 'coverflow') layoutCoverflow();
      else if (currentFx === 'stack') layoutStack();
      else if (currentFx === 'scatter') layoutScatter();
      else if (currentFx === 'masonry') { buildMasonryCols(); startMasonryScroll(); }
      else if (currentFx === 'tree') layoutTree();
      else if (currentFx === 'ring') layoutRing();
    }, 200);
  });

  /* ---------- 账户 ---------- */

  function setAuthUI() {
    var out = $('auth-out'), inn = $('auth-in');
    $('fx-bar').style.display = me ? '' : 'none';
    if (me) {
      out.classList.add('hidden');
      inn.classList.remove('hidden');
      $('who').textContent = me.display_name || me.username;
      $('nav-account').textContent = me.display_name || me.username;
      $('admin-badge').style.display = me.is_superadmin ? '' : 'none';
      $('admin-panel').style.display = me.is_superadmin ? '' : 'none';
      applyFx(me.display_effect || 'polaroid');
      if (me.is_superadmin) loadUsers();
      loadMyPhotos();
    } else {
      out.classList.remove('hidden');
      inn.classList.add('hidden');
      $('nav-account').textContent = '登录';
      setFocusMode(false);
      applyFx('polaroid');
      // 退出后清空相册，不留别人的照片。
      gallery.innerHTML = '';
      updateGalleryEmpty();
    }
  }

  /* 专注看图：只留效果选择器和照片，其他（导航、标题、情话、音乐、账户、页脚、
     飘心/闪星/花瓣）全藏起来，专心看效果；再点一次或按 Esc 退出。 */
  function setFocusMode(on) {
    document.body.classList.toggle('focus-mode', on);
    $('btn-focus').textContent = on ? '✕ 退出专注' : '🔍 专注看图';
  }
  $('btn-focus').addEventListener('click', function () {
    setFocusMode(!document.body.classList.contains('focus-mode'));
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && document.body.classList.contains('focus-mode') &&
        !$('lightbox').classList.contains('open')) {
      setFocusMode(false);
    }
  });

  function updateGalleryEmpty() {
    var empty = $('gallery-empty');
    var has = gallery.querySelectorAll('.photo-card').length > 0;
    empty.style.display = has ? 'none' : '';
    empty.textContent = me
      ? '你还没有上传照片，用上面的表单传一张吧。'
      : '登录后，这里只显示你自己上传的照片，别人看不到。';
    var n = gallery.querySelectorAll('.photo-card').length;
    var q = $('quota');
    if (q) q.textContent = '已上传 ' + n + ' / 50 张。';
  }

  // 只拉登录用户自己的照片（后端已按会话过滤）。
  function loadMyPhotos() {
    if (!me) return;
    api('/love/api/photos').then(function (j) {
      gallery.innerHTML = '';
      j.photos.forEach(function (p) {
        gallery.appendChild(photoCard(p));
      });
      updateGalleryEmpty();
      if (currentFx === 'carousel') showSlide(0);
      if (currentFx === 'coverflow') layoutCoverflow();
      if (currentFx === 'stack') layoutStack();
      if (currentFx === 'scatter') layoutScatter();
      if (currentFx === 'ring') { ringFocus = -1; layoutRing(); }
    }).catch(function () {
      updateGalleryEmpty();
    });
  }

  $('nav-account').addEventListener('click', function () {
    $('account-card').scrollIntoView({ behavior: 'smooth', block: 'center' });
  });

  $('tab-login').addEventListener('click', function () {
    $('tab-login').classList.add('active');
    $('tab-register').classList.remove('active');
    $('form-login').classList.remove('hidden');
    $('form-register').classList.add('hidden');
  });
  $('tab-register').addEventListener('click', function () {
    $('tab-register').classList.add('active');
    $('tab-login').classList.remove('active');
    $('form-register').classList.remove('hidden');
    $('form-login').classList.add('hidden');
  });

  $('form-login').addEventListener('submit', function (e) {
    e.preventDefault();
    var fd = new FormData(e.target);
    var username = String(fd.get('username') || '');
    var password = String(fd.get('password') || '');
    $('auth-msg').textContent = '登录中…';
    hashPassword(username, password).then(function (hash) {
      return api('/love/api/login', {
        method: 'POST',
        body: JSON.stringify({ username: username, password: hash }),
      });
    }).then(function (j) {
      me = j.user;
      $('auth-msg').textContent = '';
      setAuthUI();
    }).catch(function (err) {
      // 老账户存的是明文密码的哈希，新版只发 SHA-256 会对不上。
      // 一次性迁移：把刚输的密码发一次（HTTPS 加密传输），服务端验证通过后
      // 就把存库的哈希换成新格式，以后登录再也不发明文。
      if (String(err.message).indexOf('需要升级') >= 0) {
        $('auth-msg').textContent = '密码存储需要升级一次，正在用你刚输的密码完成升级…';
        api('/love/api/login', {
          method: 'POST',
          body: JSON.stringify({ username: username, password: password }),
        }).then(function (j) {
          me = j.user;
          $('auth-msg').textContent = '密码已升级到新格式，以后登录不再发送明文。';
          setAuthUI();
        }).catch(function (err2) {
          $('auth-msg').textContent = err2.message;
        });
        return;
      }
      $('auth-msg').textContent = err.message;
    });
  });

  $('form-register').addEventListener('submit', function (e) {
    e.preventDefault();
    var fd = new FormData(e.target);
    var username = String(fd.get('username') || '');
    $('auth-msg').textContent = '注册中…';
    hashPassword(username, String(fd.get('password') || '')).then(function (hash) {
      return api('/love/api/register', {
        method: 'POST',
        body: JSON.stringify({
          username: username,
          display_name: fd.get('display_name'),
          password: hash,
        }),
      });
    }).then(function (j) {
      me = j.user;
      $('auth-msg').textContent = '注册成功，欢迎！';
      setAuthUI();
    }).catch(function (err) {
      $('auth-msg').textContent = err.message;
    });
  });

  $('btn-logout').addEventListener('click', function () {
    api('/love/api/logout', { method: 'POST' }).finally(function () {
      me = null;
      setAuthUI();
    });
  });

  $('form-password').addEventListener('submit', function (e) {
    e.preventDefault();
    var fd = new FormData(e.target);
    if (!me) { $('pwd-msg').textContent = '请先登录。'; return; }
    var username = me.username;
    Promise.all([
      hashPassword(username, String(fd.get('current') || '')),
      hashPassword(username, String(fd.get('new') || '')),
    ]).then(function (hashes) {
      return api('/love/api/password', {
        method: 'POST',
        body: JSON.stringify({ current: hashes[0], new: hashes[1] }),
      });
    }).then(function () {
      $('pwd-msg').textContent = '密码已改，请重新登录。';
      me = null;
      setAuthUI();
    }).catch(function (err) {
      $('pwd-msg').textContent = err.message;
    });
  });

  /* ---------- 上传 ---------- */

  function photoCard(p) {
    var d = document.createElement('div');
    d.className = 'photo-card uploaded';
    var img = document.createElement('img');
    // 画廊只加载 800px 的缩略图（thumb_url），原图留给灯箱，加载快得多；
    // 老照片没有缩略图时用原图兜底。
    img.src = p.thumb_url || p.url;
    img.dataset.full = p.url;
    img.alt = p.caption || p.filename;
    img.loading = 'lazy';
    // 满幅行需要宽高比：图片加载后把 --ar 写到卡片上。
    var setAr = function () {
      if (img.naturalWidth && img.naturalHeight) {
        d.style.setProperty('--ar', (img.naturalWidth / img.naturalHeight).toFixed(2));
      }
    };
    if (img.complete) setAr();
    else img.addEventListener('load', setAr);
    var badge = document.createElement('span');
    badge.className = 'heart-badge';
    badge.textContent = '💗';
    var cap = document.createElement('p');
    cap.className = 'cap';
    cap.textContent = p.caption || p.filename;
    var small = document.createElement('small');
    small.textContent = (p.display_name || p.username) + ' · ' + new Date(p.created_at).toLocaleDateString();
    cap.appendChild(small);
    d.appendChild(img);
    d.appendChild(badge);
    d.appendChild(cap);
    return d;
  }

  $('form-upload').addEventListener('submit', function (e) {
    e.preventDefault();
    var fd = new FormData(e.target);
    var file = fd.get('photo');
    if (!file || !file.size) {
      $('upload-msg').textContent = '请先选一张图片。';
      return;
    }
    $('upload-msg').textContent = '正在压缩图片…';
    compressImage(file).then(function (small) {
      var up = new FormData();
      up.append('photo', small);
      up.append('caption', fd.get('caption') || '');
      $('upload-msg').textContent = '上传中…';
      return fetch('/love/api/upload', { method: 'POST', body: up })
        .then(function (r) { return r.json().then(function (j) {
          if (!r.ok) throw new Error(j.error || ('上传失败 HTTP ' + r.status));
          return j;
        }); });
    })
      .then(function (j) {
        $('upload-msg').textContent = '上传成功，已存入对象存储。';
        e.target.reset();
        gallery.insertBefore(photoCard(j.photo), gallery.firstChild);
        updateGalleryEmpty();
        if (currentFx === 'carousel') showSlide(0);
        if (currentFx === 'coverflow') layoutCoverflow();
        if (currentFx === 'stack') layoutStack();
        if (currentFx === 'scatter') layoutScatter();
        if (currentFx === 'masonry') { buildMasonryCols(); startMasonryScroll(); }
        if (currentFx === 'tree') layoutTree();
        if (currentFx === 'ring') { ringFocus = -1; layoutRing(); }
      })
      .catch(function (err) {
        $('upload-msg').textContent = err.message;
      });
  });

  /* ---------- 管理员：看用户 ---------- */

  function loadUsers() {
    api('/love/api/users').then(function (j) {
      var ul = $('user-list');
      ul.innerHTML = '';
      j.users.forEach(function (u) {
        var li = document.createElement('li');
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'user-btn';
        btn.textContent = (u.is_superadmin ? '👑 ' : '') + (u.display_name || u.username) +
          '（' + u.username + '）· ' + u.photo_count + ' 张';
        btn.addEventListener('click', function () { loadUserPhotos(u); });
        li.appendChild(btn);
        ul.appendChild(li);
      });
    }).catch(function () { /* 管理员接口失败时不打扰 */ });
  }

  function loadUserPhotos(u) {
    var box = $('admin-photos');
    box.innerHTML = '<p class="hint">正在看 ' + (u.display_name || u.username) + ' 的照片…</p>';
    api('/love/api/photos?user_id=' + u.id).then(function (j) {
      box.innerHTML = '';
      var h = document.createElement('h5');
      h.textContent = (u.display_name || u.username) + ' 的照片（' + j.photos.length + ' 张）';
      box.appendChild(h);
      if (!j.photos.length) {
        var p = document.createElement('p');
        p.className = 'hint';
        p.textContent = '这个用户还没上传照片。';
        box.appendChild(p);
        return;
      }
      var grid = document.createElement('div');
      grid.className = 'admin-photos-grid';
      j.photos.forEach(function (ph) {
        var a = document.createElement('a');
        a.href = ph.url;
        a.target = '_blank';
        a.rel = 'noopener';
        var img = document.createElement('img');
        img.src = ph.thumb_url || ph.url;
        img.alt = ph.caption || ph.filename;
        img.loading = 'lazy';
        a.appendChild(img);
        grid.appendChild(a);
      });
      box.appendChild(grid);
    }).catch(function (err) {
      box.innerHTML = '<p class="hint">' + err.message + '</p>';
    });
  }

  /* ---------- 启动 ---------- */

  // 先看登录状态：登录后才拉自己的照片（未登录不显示任何照片）。
  api('/love/api/me').then(function (j) {
    me = j.user;
    setAuthUI();
  }).catch(function () {
    me = null;
    setAuthUI();
  });
})();
