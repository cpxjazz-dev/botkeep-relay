/* love 站前端：账户、上传、展示效果、管理员。只调 /love/api/*。 */
(function () {
  var gallery = document.getElementById('gallery');
  var carouselBar = document.getElementById('carousel-bar');
  var carCount = document.getElementById('car-count');
  var FX = ['polaroid', 'grid', 'carousel', 'masonry'];
  var currentFx = 'polaroid';
  var carIndex = 0;
  var carTimer = null;
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

  /* ---------- 展示效果 ---------- */

  function applyFx(fx) {
    if (FX.indexOf(fx) < 0) fx = 'polaroid';
    currentFx = fx;
    gallery.classList.remove('fx-polaroid', 'fx-grid', 'fx-carousel', 'fx-masonry');
    gallery.classList.add('fx-' + fx);
    carouselBar.style.display = fx === 'carousel' ? 'flex' : 'none';
    document.querySelectorAll('#fx-picker .fx-btn').forEach(function (b) {
      b.classList.toggle('active', b.getAttribute('data-fx') === fx);
    });
    if (fx === 'carousel') {
      carIndex = 0;
      showSlide(0);
      startAuto();
    } else {
      stopAuto();
      // 轮播时藏起来的卡片要恢复显示。
      gallery.querySelectorAll('.photo-card').forEach(function (c) {
        c.style.display = '';
      });
    }
  }

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

  $('car-prev').addEventListener('click', function () { showSlide(carIndex - 1); startAuto(); });
  $('car-next').addEventListener('click', function () { showSlide(carIndex + 1); startAuto(); });

  document.querySelectorAll('#fx-picker .fx-btn').forEach(function (b) {
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

  /* ---------- 账户 ---------- */

  function setAuthUI() {
    var out = $('auth-out'), inn = $('auth-in');
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
      applyFx('polaroid');
      // 退出后清空相册，不留别人的照片。
      gallery.innerHTML = '';
      updateGalleryEmpty();
    }
  }

  function updateGalleryEmpty() {
    var empty = $('gallery-empty');
    var has = gallery.querySelectorAll('.photo-card').length > 0;
    empty.style.display = has ? 'none' : '';
    empty.textContent = me
      ? '你还没有上传照片，用上面的表单传一张吧。'
      : '登录后，这里只显示你自己上传的照片，别人看不到。';
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
    $('auth-msg').textContent = '登录中…';
    api('/love/api/login', {
      method: 'POST',
      body: JSON.stringify({ username: fd.get('username'), password: fd.get('password') }),
    }).then(function (j) {
      me = j.user;
      $('auth-msg').textContent = '';
      setAuthUI();
    }).catch(function (err) {
      $('auth-msg').textContent = err.message;
    });
  });

  $('form-register').addEventListener('submit', function (e) {
    e.preventDefault();
    var fd = new FormData(e.target);
    $('auth-msg').textContent = '注册中…';
    api('/love/api/register', {
      method: 'POST',
      body: JSON.stringify({
        username: fd.get('username'),
        display_name: fd.get('display_name'),
        password: fd.get('password'),
      }),
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
    api('/love/api/password', {
      method: 'POST',
      body: JSON.stringify({ current: fd.get('current'), new: fd.get('new') }),
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
    img.src = p.url;
    img.alt = p.caption || p.filename;
    img.loading = 'lazy';
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
    if (!fd.get('photo') || !fd.get('photo').size) {
      $('upload-msg').textContent = '请先选一张图片。';
      return;
    }
    $('upload-msg').textContent = '上传中…';
    fetch('/love/api/upload', { method: 'POST', body: fd })
      .then(function (r) { return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || ('上传失败 HTTP ' + r.status));
        return j;
      }); })
      .then(function (j) {
        $('upload-msg').textContent = '上传成功，已存入对象存储。';
        e.target.reset();
        gallery.insertBefore(photoCard(j.photo), gallery.firstChild);
        updateGalleryEmpty();
        if (currentFx === 'carousel') showSlide(0);
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
        img.src = ph.url;
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
