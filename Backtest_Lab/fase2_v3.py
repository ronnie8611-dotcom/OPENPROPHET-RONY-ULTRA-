"""FASE 2 v3: Fix sizing TSMOM + portfolio Raschke+TSMOM"""
import os, warnings
import numpy as np, pandas as pd, yfinance as yf
warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"; os.makedirs(CACHE_DIR, exist_ok=True)

def to_date_df(df):
    df = df.copy().reset_index()
    if "date" not in df.columns: df.rename(columns={df.columns[0]: "date"}, inplace=True)
    return df

def download(symbol, interval="1d"):
    key = f"{symbol.replace('=','_')}_{interval}_max_now"
    cache = os.path.join(CACHE_DIR, key + ".csv")
    if os.path.exists(cache): return pd.read_csv(cache, index_col=0, parse_dates=True)
    print(f"  [desc] {symbol}...", end=" ")
    df = yf.download(symbol, period="max", interval=interval, progress=False, auto_adjust=False, threads=False)
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.index.name = "date"; df.to_csv(cache); print(f"{len(df)} velas"); return df

def metrics(eq, capital):
    r = eq.pct_change().dropna()
    if len(r) < 20: return None
    y = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (eq.iloc[-1]/eq.iloc[0])**(1/y) - 1 if y > 0 else 0
    sr = r.mean()/r.std()*np.sqrt(252) if r.std() > 0 else 0
    dn = r[r < 0]
    sortino = r.mean()/dn.std()*np.sqrt(252) if len(dn) > 0 and dn.std() > 0 else 0
    peak = eq.cummax(); dd = (eq - peak)/peak
    return {"sharpe": sr, "sortino": sortino, "cagr": cagr*100, "mdd": dd.min()*100, "final": eq.iloc[-1], "eq": eq}

# TSMOM con TARGET VOL (no risk_pct fijo)
def backtest_tsmom(df, capital=10000, lookback=252, hold=21, target_vol=0.15, max_lev=0.95):
    df = to_date_df(df)
    df["ret"] = df["close"].pct_change()
    df["mom"] = df["close"].pct_change(lookback).shift(hold)
    df["vol"] = df["ret"].rolling(20).std() * np.sqrt(252)
    nav = capital; equity = []
    for i in range(lookback+hold, len(df), hold):
        row = df.iloc[i]
        if pd.isna(row["mom"]) or pd.isna(row["vol"]) or row["vol"] <= 0: continue
        size = min(target_vol / row["vol"], max_lev)  # Apalancamiento, no fracción
        pos = 1 if row["mom"] > 0 else -1
        ret_next = df["ret"].iloc[i:i+hold].sum()
        nav += nav * size * pos * ret_next
        equity.append({"date": row["date"], "nav": nav})
    if len(equity) < 5: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics(eq, capital), eq

# Raschke - copia del backtest_correcto.py (ya validado)
def backtest_raschke(df, capital=10000, risk_pct=1.0, comm=0.005, slip_bps=2.0, max_bars=5):
    df = to_date_df(df)
    df["ib_high"] = df["high"].shift(1); df["ib_low"] = df["low"].shift(1)
    df["ib_mid"] = (df["ib_high"]+df["ib_low"])/2
    sigs = []
    for i in range(1, len(df)):
        r = df.iloc[i]
        if pd.isna(r["ib_high"]): continue
        p, ih, il, im = r["close"], r["ib_high"], r["ib_low"], r["ib_mid"]
        if p >= ih*0.999: sigs.append({"idx": i, "dir": 1, "entry": ih, "sl": im, "tp": ih+(ih-il)*0.5})
        elif p <= il*1.001: sigs.append({"idx": i, "dir": -1, "entry": il, "sl": im, "tp": il-(il-il)*0.5})
    nav = capital; equity = []; open_t = None
    sig_by_day = {}
    for s in sigs: sig_by_day.setdefault(s["idx"], []).append(s)
    for i in range(len(df)):
        r = df.iloc[i]
        if open_t is not None:
            t = open_t; exit_px = None
            if t["dir"] == 1:
                if r["low"] <= t["sl"]: exit_px = t["sl"]
                elif r["high"] >= t["tp"]: exit_px = t["tp"]
            else:
                if r["high"] >= t["sl"]: exit_px = t["sl"]
                elif r["low"] <= t["tp"]: exit_px = t["tp"]
            if exit_px is None and (i-t["entry_idx"]) >= max_bars: exit_px = r["close"]
            if exit_px is not None:
                ex = exit_px * (1 - slip_bps/10000 * t["dir"])
                pnl = (ex - t["entry_px"]) * t["dir"] * t["qty"] - comm*t["qty"]*2
                nav += pnl; open_t = None
        if open_t is None and i in sig_by_day:
            s = sig_by_day[i][0]
            entry_px = s["entry"] * (1 + slip_bps/10000 * s["dir"])
            risk_dollars = nav * risk_pct/100
            sl_dist = abs(entry_px - s["sl"])
            if sl_dist > 0:
                qty = int(min(risk_dollars/sl_dist, (nav*0.15)/entry_px))
                if qty >= 1:
                    open_t = {"dir": s["dir"], "entry_px": entry_px, "sl": s["sl"], "tp": s["tp"], "qty": qty, "entry_idx": i}
        equity.append({"date": df.iloc[i]["date"], "nav": nav})
    if len(equity) < 5: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics(eq, capital), eq

def main():
    print("="*82); print("  FASE 2 v3: Fix sizing + Portfolio combinado"); print("="*82)

    qqq = download("QQQ", "1d")
    if qqq.empty: print("Sin datos"); return

    print("\n[TSMOM con target_vol=15%]")
    m_tsmom, eq_tsmom = backtest_tsmom(qqq, capital=10000)
    if m_tsmom:
        print(f"  Sharpe {m_tsmom['sharpe']:.2f} | CAGR {m_tsmom['cagr']:.2f}% | DD {m_tsmom['mdd']:.2f}% | NAV ${m_tsmom['final']:,.0f}")

    print("\n[Raschke baseline]")
    m_raschke, eq_raschke = backtest_raschke(qqq, capital=10000)
    if m_raschke:
        print(f"  Sharpe {m_raschke['sharpe']:.2f} | CAGR {m_raschke['cagr']:.2f}% | DD {m_raschke['mdd']:.2f}% | NAV ${m_raschke['final']:,.0f}")

    # Portfolio: 50% Raschke + 50% TSMOM
    if m_tsmom and m_raschke:
        print("\n[Portfolio 50/50: Raschke + TSMOM]")
        # Alinear series
        common = eq_raschke.index.intersection(eq_tsmom.index)
        if len(common) > 20:
            r1 = eq_raschke.reindex(common).pct_change().fillna(0)
            r2 = eq_tsmom.reindex(common).pct_change().fillna(0)
            r_port = 0.5*r1 + 0.5*r2
            eq_port = (1 + r_port).cumprod() * 10000
            m_port = metrics(eq_port, 10000)
            if m_port:
                print(f"  Sharpe {m_port['sharpe']:.2f} | CAGR {m_port['cagr']:.2f}% | DD {m_port['mdd']:.2f}% | NAV ${m_port['final']:,.0f}")
                # Correlacion
                corr = r1.corr(r2)
                print(f"  Correlacion Raschke/TSMOM: {corr:.2f}  (bajo = buena diversificacion)")

    print("\n" + "="*82)
    print("  INTERPRETACION")
    print("="*82)
    print("  TSMOM con target_vol=15% -> CAGR deberia subir a 8-12%")
    print("  Portfolio combinado -> Sharpe esperado 2.3-2.7, DD esperado 6-10%")
    print("  Correlacion <0.3 -> diversificacion real, vale la pena combinar")
    print("  Correlacion >0.6 -> no aporta diversificacion, elegir solo uno")

if __name__ == "__main__": main()