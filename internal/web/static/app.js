// Ziba's only JavaScript.
//
// Everything here is progressive enhancement: each feature improves something
// that already works without it. With this file blocked or failing to load, the
// date picker still has its button and marking read still posts and redirects.
// Nothing below is required to use the site.

(function () {
  'use strict';

  // Choosing a date navigates, instead of asking for a second click on "Show".
  var datePicker = document.querySelector('.daypick input[type=date]');
  if (datePicker) {
    datePicker.addEventListener('change', function () {
      this.form.submit();
    });
  }

  // Marking read happens in place.
  //
  // The plain form posts and redirects back, which reloads the page and returns
  // the reader to the top of it — so marking the fourth article read means
  // scrolling back down to reach the fifth. Here the post is sent in the
  // background and the card is updated where it sits.
  //
  // Listening on the document rather than on each form: one listener, and it
  // keeps working for anything added to the page later.
  document.addEventListener('submit', function (event) {
    var form = event.target;
    if (!form.classList.contains('inline')) {
      return;
    }
    event.preventDefault();

    var button = form.querySelector('button');
    button.disabled = true;

    fetch(form.action, {
      method: 'POST',
      body: new FormData(form),
      // Tells the handler to answer with a bare 204 rather than the redirect a
      // browser form needs.
      headers: { 'X-Ziba-Async': '1' },
      credentials: 'same-origin'
    }).then(function (response) {
      if (!response.ok) {
        showArchiveError(form, button);
        return;
      }
      button.disabled = false;
      // Every control for this article, not just the one clicked: the reader
      // shows the same button at the head and the foot of the page, and one
      // flipping while the other did not would be worse than a reload.
      var article = articleOf(form);
      document.querySelectorAll('form.inline').forEach(function (other) {
        if (articleOf(other) === article) {
          toggle(other, other.querySelector('button'));
        }
      });
    }, function () {
      showArchiveError(form, button);
    });
  });

  // Once the script has intercepted a submission it must never submit the same
  // form again: the request may have reached the server even when its response
  // was lost. Retrying automatically could therefore perform the action twice
  // and, more visibly, bring back the full-page reload this enhancement avoids.
  function showArchiveError(form, button) {
    button.disabled = false;
    var error = form.querySelector('.action-error');
    if (!error) {
      error = document.createElement('span');
      error.className = 'action-error';
      error.setAttribute('role', 'alert');
      form.appendChild(error);
    }
    error.textContent = 'Could not update. Try again.';
  }

  // articleOf reads the article's id out of a form's address, which is either
  // /article/{id}/archive or /article/{id}/unarchive.
  function articleOf(form) {
    var parts = form.getAttribute('action').split('/');
    return parts[2];
  }

  // toggle swaps the control to its opposite state.
  //
  // Both labels come from the template, carried on the button as data
  // attributes, so the wording lives in one place and this file does not have
  // to keep a copy of it in step.
  function toggle(form, button) {
    var label = button.textContent;
    var title = button.title;
    button.textContent = button.dataset.altLabel;
    button.title = button.dataset.altTitle;
    button.dataset.altLabel = label;
    button.dataset.altTitle = title;

    var read = form.getAttribute('action').indexOf('/unarchive') === -1;
    form.setAttribute('action',
      form.getAttribute('action').replace(/\/(un)?archive$/, read ? '/unarchive' : '/archive'));

    // Dim the card so a glance down the page shows what has been dealt with.
    // The reader view has no card, and needs none: the button says it.
    var card = form.closest('.card');
    if (card) {
      card.classList.toggle('read', read);
    }
  }

  document.addEventListener('change', function (event) {
    if (event.target.matches('[data-source-type]')) {
      toggleSourceFields(event.target.closest('[data-source-card]'));
    }
  });

  document.querySelectorAll('[data-source-card]').forEach(toggleSourceFields);

  var linkwardenAuth = document.querySelector('[data-linkwarden-auth]');
  if (linkwardenAuth) {
    linkwardenAuth.addEventListener('change', toggleLinkwardenAuth);
    toggleLinkwardenAuth();
  }

  document.querySelectorAll('[data-tag-picker]').forEach(function (picker) {
    var input = picker.querySelector('[data-tag-input]');
    var selected = picker.querySelector('[data-selected-tags]');
    var form = picker.closest('form');

    function addTag(raw) {
      var name = raw.trim();
      if (!name) return;
      var duplicate = false;
      selected.querySelectorAll('[data-tag]').forEach(function (tag) {
        if (tag.dataset.tag.toLowerCase() === name.toLowerCase()) duplicate = true;
      });
      if (duplicate) {
        input.value = '';
        return;
      }

      var chip = document.createElement('span');
      chip.className = 'selected-tag';
      chip.dataset.tag = name;
      var label = document.createElement('span');
      label.textContent = name;
      var remove = document.createElement('button');
      remove.type = 'button';
      remove.textContent = '×';
      remove.title = 'Remove ' + name;
      remove.setAttribute('aria-label', remove.title);
      var hidden = document.createElement('input');
      hidden.type = 'hidden';
      hidden.name = 'tag_names';
      hidden.value = name;
      chip.appendChild(label);
      chip.appendChild(remove);
      chip.appendChild(hidden);
      selected.appendChild(chip);
      input.value = '';
    }

    input.addEventListener('keydown', function (event) {
      if (event.key === 'Enter' || event.key === ',') {
        event.preventDefault();
        addTag(input.value.replace(/,$/, ''));
      }
    });
    input.addEventListener('change', function () { addTag(input.value); });
    selected.addEventListener('click', function (event) {
      if (event.target.matches('button')) event.target.closest('[data-tag]').remove();
    });
    form.addEventListener('submit', function () { addTag(input.value); });
  });

  // Collection is global state, shown above the interest tabs. Polling keeps
  // that small status current; only an empty home page reloads after a run, so
  // reading and forms are never interrupted.
  var collectionStatus = document.querySelector('[data-collection-status]');
  if (collectionStatus) {
    var wasRunning = collectionStatus.dataset.running === 'true';
    var completed = Number(collectionStatus.dataset.completed || 0);
    var pollTimer;

    function scheduleStatusPoll(delay) {
      clearTimeout(pollTimer);
      pollTimer = setTimeout(pollCollectionStatus, delay);
    }

    function pollCollectionStatus() {
      if (document.hidden) return;
      fetch('/status', { credentials: 'same-origin' }).then(function (response) {
        if (!response.ok) throw new Error('unexpected status ' + response.status);
        return response.json();
      }).then(function (status) {
        var text = status.running ? 'Collecting and preparing your digest…' :
          (status.disabled ? 'Automatic collection is off.' : status.next_label);
        var textNode = collectionStatus.querySelector('[data-collection-status-text]');
        if (textNode.textContent !== text) textNode.textContent = text;
        collectionStatus.querySelector('.collection-dot').classList.toggle('running', status.running);

        var finished = (wasRunning && !status.running) || status.completed > completed;
        wasRunning = status.running;
        completed = status.completed;
        if (finished && document.querySelector('[data-empty-digest]')) {
          window.location.reload();
          return;
        }
        scheduleStatusPoll(status.running ? 3000 : 10000);
      }).catch(function () {
        scheduleStatusPoll(30000);
      });
    }

    document.addEventListener('visibilitychange', function () {
      if (!document.hidden) pollCollectionStatus();
      else clearTimeout(pollTimer);
    });
    scheduleStatusPoll(wasRunning ? 3000 : 10000);
  }

  function toggleSourceFields(card) {
    if (!card) return;
    var type = card.querySelector('[data-source-type]').value;
    card.querySelectorAll('[data-fields]').forEach(function (fields) {
      var hidden = fields.dataset.fields !== type;
      fields.hidden = hidden;
      fields.querySelectorAll('input, select, textarea').forEach(function (field) {
        field.disabled = hidden;
      });
    });
  }

  function toggleLinkwardenAuth() {
    document.querySelectorAll('[data-linkwarden-fields]').forEach(function (fields) {
      var hidden = fields.dataset.linkwardenFields !== linkwardenAuth.value;
      fields.hidden = hidden;
      fields.querySelectorAll('input').forEach(function (field) {
        field.disabled = hidden;
      });
    });
  }

})();
