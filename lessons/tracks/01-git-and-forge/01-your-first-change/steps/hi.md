---
title: आपका पहला change
---

## चरण 1 — Clone करें और branch बनाएँ

Ore ने इस पाठ के लिए एक repository बनाई है (इसका नाम `learn-your-first-change-` से शुरू होता है)। इसे clone करें और अपने काम के लिए एक branch बनाएँ। Ore इस branch पर नज़र रखता है; और कुछ करने की ज़रूरत नहीं है।

```bash
git clone <your repository url>
cd <your repository>
git switch -c learn/first-change-$(whoami)
git push -u origin HEAD
```

**Branch क्यों?** default branch नीति (policy) से सुरक्षित है। बदलाव वहाँ केवल review के ज़रिए पहुँचते हैं। एक branch आपका निजी कार्यक्षेत्र है, जहाँ आपका कोई भी काम किसी और को प्रभावित नहीं कर सकता।

> संकेत (दो असफल जाँचों के बाद दिखता है): branch को Ore पर push करना ज़रूरी है, केवल स्थानीय रूप से बनाना काफ़ी नहीं: `git push -u origin HEAD`। इसका नाम `learn/first-change-` से शुरू होना चाहिए।

> संकेत: अगर `git switch -c` काम न करे, तो आपका git 2.23 से पुराना है; इसके बजाय `git checkout -b learn/first-change-$(whoami)` चलाएँ।

## चरण 2 — Commit करें और push करें

एक फ़ाइल बनाएँ, उसे commit करें, और branch को push करें।

```bash
mkdir -p learn
echo "Hello from $(whoami)" > learn/hello.md
git add learn/hello.md
git commit -s -m "learn: add hello.md"
git push
```

**`-s` क्यों?** यह एक `Signed-off-by:` पंक्ति जोड़ता है, जो Developer Certificate of Origin को प्रमाणित करती है। कई परियोजनाएँ इसकी माँग करती हैं; Ore इसे ledger पर दर्ज करता है।

> संकेत: फ़ाइल ठीक `learn/hello.md` होनी चाहिए, आपकी `learn/first-change-*` branch पर commit की हुई और आपकी अपनी credentials से push की हुई (पाठ जाँचता है कि push किसने किया)।

> संकेत: "protected ref" कहकर push अस्वीकार हो, तो आप default branch पर हैं। चरण 1 पर लौटें और अपनी branch पर जाएँ।

## चरण 3 — एक change खोलें

अपनी branch से default branch की ओर एक change खोलें। UI का **New change** बटन इस्तेमाल करें, या API:

```bash
ore change open <org>/<repo> --source learn/first-change-$(whoami) --title "learn: add hello.md"
```

Change review की इकाई है। इसकी एक स्थायी id होती है, एक या अधिक patch set, pipeline के परिणाम, और वह नीति जो तय करती है कि merge से पहले इसे क्या चाहिए। change का पृष्ठ देखें: वह आपको ठीक-ठीक बताता है कि क्या आवश्यक है।

> संकेत: change का source आपकी `learn/first-change-*` branch होना चाहिए और target default branch (जब आप `--target` न दें, तो यही default है)।

## चरण 4 — इसे merge करें

Change को merge करें। अगर नीति को reviewer चाहिए, तो किसी साथी या अपने mentor से कहें; change का पृष्ठ दिखाता है कि कौन approve कर सकता है।

```bash
ore change merge <org>/<repo> 1
```

जब change merge हो जाता है, पाठ पूरा हो जाता है और आपकी प्रगति का credential change id को प्रमाण के रूप में जोड़कर अद्यतन होता है।

**आपने क्या किया**: एक branch, एक signed commit, एक change, एक merge। यही वह पूरा चक्र है जिससे यहाँ हर change गुज़रता है।

> संकेत: जब change साफ़ तरीके से merge न हो, तो merge अस्वीकार होता है; `git fetch` करें, default branch को अपनी branch में merge करें, फिर से push करें, और फिर change को merge करें।
