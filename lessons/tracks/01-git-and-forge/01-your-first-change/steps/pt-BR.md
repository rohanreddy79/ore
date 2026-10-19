---
title: Sua primeira change
---

## Passo 1 — Clonar e criar um branch

O Ore criou um repositório para esta lição (o nome começa com `learn-your-first-change-`). Clone-o e crie um branch para o seu trabalho. O Ore observa o branch; nada mais é necessário.

```bash
git clone <your repository url>
cd <your repository>
git switch -c learn/first-change-$(whoami)
git push -u origin HEAD
```

**Por que um branch?** O branch padrão é protegido por política. Mudanças só chegam lá por meio de revisão. Um branch é um espaço de trabalho privado, onde nada do que você faz afeta outra pessoa.

> Dica (mostrada após duas verificações falhas): o branch precisa ser enviado ao Ore, não apenas criado localmente: `git push -u origin HEAD`. O nome deve começar com `learn/first-change-`.

> Dica: se `git switch -c` falhar, seu git é anterior à versão 2.23; use `git checkout -b learn/first-change-$(whoami)`.

## Passo 2 — Fazer commit e push

Crie um arquivo, faça o commit e envie o branch.

```bash
mkdir -p learn
echo "Hello from $(whoami)" > learn/hello.md
git add learn/hello.md
git commit -s -m "learn: add hello.md"
git push
```

**Por que `-s`?** Ele adiciona uma linha `Signed-off-by:` certificando o Developer Certificate of Origin. Muitos projetos exigem isso; o Ore registra no ledger.

> Dica: o arquivo deve ser exatamente `learn/hello.md`, com commit no seu branch `learn/first-change-*` e enviado com as suas próprias credenciais (a lição verifica quem fez o push).

> Dica: um push recusado com "protected ref" significa que você está no branch padrão. Volte ao passo 1 e mude para o seu branch.

## Passo 3 — Abrir uma change

Abra uma change do seu branch para o branch padrão. Use o botão **New change** da interface, ou a API:

```bash
ore change open <org>/<repo> --source learn/first-change-$(whoami) --title "learn: add hello.md"
```

Uma change é a unidade de revisão. Ela tem um id estável, um ou mais patch sets, os resultados da pipeline e a política que decide o que ela precisa antes de poder ser mesclada. Olhe a página da change: ela diz exatamente o que é exigido.

> Dica: a origem da change deve ser o seu branch `learn/first-change-*` e o destino o branch padrão (o padrão quando você omite `--target`).

## Passo 4 — Mesclar

Mescle a change. Se a política exigir um revisor, peça a um colega ou ao seu mentor; a página da change mostra quem pode aprovar.

```bash
ore change merge <org>/<repo> 1
```

Quando a change é mesclada, a lição termina e a sua credencial de progresso é atualizada com o id da change como evidência.

**O que você fez**: um branch, um commit assinado, uma change, um merge. Esse é o ciclo completo pelo qual toda change passa aqui.

> Dica: um merge é recusado quando a change não mescla de forma limpa; faça `git fetch`, mescle o branch padrão no seu branch, envie de novo e então mescle a change.
