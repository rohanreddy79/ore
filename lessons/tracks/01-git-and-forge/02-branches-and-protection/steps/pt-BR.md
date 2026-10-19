---
title: Branches e proteção
---

## Passo 1 — Veja a recusa, depois crie um branch

Clone o repositório da lição (o nome começa com `learn-branches-and-protection-`), faça um commit no branch padrão e tente enviá-lo. Observe a recusa: o branch padrão é protegido, e o hook pre-receive do Ore diz isso.

```bash
git clone <your repository url>
cd <your repository>
echo "direct push" >> README.md
git commit -am "learn: try a direct push"
git push                       # refused: main is protected; push a branch and open a change instead
```

Agora faça do jeito certo: mova o seu commit para um branch e envie esse branch.

```bash
git switch -c learn/protect-$(whoami)
git push -u origin HEAD
```

**Por que o branch padrão é protegido?** Porque tudo o que chega a ele deveria ter sido revisado e verificado. Proteção é uma política, não um cadeado: ela redireciona o trabalho para changes, onde revisão, pipelines e o ledger se aplicam.

> Dica (mostrada após duas verificações falhas): a recusa é esperada; a verificação procura um branch chamado `learn/protect-<algo>` enviado ao Ore. Execute `git switch -c learn/protect-$(whoami)` e `git push -u origin HEAD`.

> Dica: `git switch -c` mantém o seu commit local no novo branch, então nada se perde; `git log --oneline -2` o mostra.

## Passo 2 — Descreva a proteção

No seu branch, adicione `learn/branches.md` descrevendo o que você viu. A lição verifica que o arquivo menciona a palavra *protected*.

```bash
cat > learn/branches.md <<'TXT'
The default branch is protected: direct pushes are refused.
Work goes to a branch and lands through a change.
TXT
git add learn/branches.md
git commit -s -m "learn: describe branch protection"
git push
```

> Dica: o arquivo é `learn/branches.md` e deve conter a palavra `protected` (em minúsculas), no seu branch `learn/protect-*`.

> Dica: você fez o push? `git status` diz "Your branch is ahead of 'origin/...'" quando ainda falta enviar.

## Passo 3 — Abra uma change

Abra uma change do seu branch para o branch padrão.

```bash
ore change open <org>/<repo> --source learn/protect-$(whoami) --title "learn: describe branch protection"
```

A página da change lista a política que a change precisa satisfazer: resultados da pipeline, aprovações, nenhum conflito. É a essa política que a proteção delega.

> Dica: a origem deve ser o seu branch `learn/protect-*`; o destino é o branch padrão por padrão.

## Passo 4 — Faça o merge

Mescle a change. O merge é feito do lado do servidor pelo Ore, a única coisa autorizada a mover o branch protegido.

```bash
ore change merge <org>/<repo> 1
```

A lição termina quando o branch padrão contém `learn/branches.md`.

> Dica: se o merge for recusado por conflito, atualize o seu branch a partir do branch padrão e envie de novo.
