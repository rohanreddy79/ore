---
title: Branch और सुरक्षा
---

## चरण 1 — अस्वीकृति देखें, फिर branch बनाएँ

पाठ की repository clone करें (इसका नाम `learn-branches-and-protection-` से शुरू होता है), default branch पर एक commit करें और उसे push करने की कोशिश करें। अस्वीकृति देखें: default branch सुरक्षित (protected) है, और Ore का pre-receive hook यही कहता है।

```bash
git clone <your repository url>
cd <your repository>
echo "direct push" >> README.md
git commit -am "learn: try a direct push"
git push                       # refused: main is protected; push a branch and open a change instead
```

अब इसे सही तरीके से करें: अपने commit को एक branch पर ले जाएँ और उसे push करें।

```bash
git switch -c learn/protect-$(whoami)
git push -u origin HEAD
```

**Default branch सुरक्षित क्यों है?** क्योंकि वहाँ पहुँचने वाली हर चीज़ की समीक्षा और जाँच हो चुकी होनी चाहिए। सुरक्षा एक नीति है, ताला नहीं: यह काम को change के रास्ते मोड़ती है, जहाँ review, pipeline और ledger लागू होते हैं।

> संकेत (दो असफल जाँचों के बाद दिखता है): अस्वीकृति अपेक्षित है; जाँच `learn/protect-<कुछ>` नाम की एक branch खोजती है जो Ore पर push की गई हो। `git switch -c learn/protect-$(whoami)` और `git push -u origin HEAD` चलाएँ।

> संकेत: `git switch -c` आपके स्थानीय commit को नई branch पर रखता है, इसलिए कुछ खोता नहीं; `git log --oneline -2` उसे दिखाता है।

## चरण 2 — सुरक्षा का वर्णन करें

अपनी branch पर `learn/branches.md` जोड़ें, जिसमें आपने जो देखा उसका वर्णन हो। पाठ जाँचता है कि फ़ाइल में *protected* शब्द है।

```bash
cat > learn/branches.md <<'TXT'
The default branch is protected: direct pushes are refused.
Work goes to a branch and lands through a change.
TXT
git add learn/branches.md
git commit -s -m "learn: describe branch protection"
git push
```

> संकेत: फ़ाइल `learn/branches.md` है और उसमें `protected` शब्द (छोटे अक्षरों में) होना चाहिए, आपकी `learn/protect-*` branch पर।

> संकेत: क्या आपने push किया? जब push बाकी हो, `git status` कहता है "Your branch is ahead of 'origin/...'"।

## चरण 3 — एक change खोलें

अपनी branch से default branch की ओर एक change खोलें।

```bash
ore change open <org>/<repo> --source learn/protect-$(whoami) --title "learn: describe branch protection"
```

Change का पृष्ठ वह नीति सूचीबद्ध करता है जिसे change को पूरा करना है: pipeline के परिणाम, approvals, कोई conflict नहीं। सुरक्षा इसी नीति को काम सौंपती है।

> संकेत: source आपकी `learn/protect-*` branch होनी चाहिए; target default रूप से default branch होता है।

## चरण 4 — इसे land करें

Change को merge करें। merge Ore द्वारा server की ओर से किया जाता है, और केवल Ore को ही सुरक्षित branch को आगे बढ़ाने की अनुमति है।

```bash
ore change merge <org>/<repo> 1
```

जब default branch में `learn/branches.md` आ जाता है, पाठ पूरा हो जाता है।

> संकेत: अगर conflict की वजह से merge अस्वीकार हो, तो अपनी branch को default branch से अद्यतन करें और फिर से push करें।
